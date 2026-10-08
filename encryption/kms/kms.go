// Package kms provides an AWS KMS encryption provider for field-level encryption.
// It uses KMS for envelope encryption: GenerateDataKey creates DEKs via KMS,
// and Decrypt unwraps them. Field-level encryption uses the plaintext DEK locally.
//
// # Key identifiers
//
// The keyID passed to every method is forwarded to AWS as the KeyId parameter and
// stamped into event metadata ($encryption_key_id) by the event store. Stamp key
// ARNs (or bare key ids), not aliases: an alias ("alias/...") is a mutable pointer
// that can be re-targeted after the event was written, so a later revocation or
// revocation probe would act on whatever key the alias happens to point at today.
// The revocation methods defensively resolve whatever id they are given through
// DescribeKey and operate on the canonical KeyMetadata.KeyId (ScheduleKeyDeletion
// and CancelKeyDeletion reject aliases outright), but the ARN recorded at write
// time is the only id that is guaranteed to name the key the data was sealed under.
package kms

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"go-mink.dev/encryption"
)

// DefaultRevocationTimeout bounds the KMS calls made by the context-free
// revocation methods (RevokeKey, IsRevoked, RevocationState, SoftRevokeKey,
// UnrevokeKey) when no WithRevocationTimeout is configured, so an unreachable KMS
// endpoint cannot hang an erasure forever.
const DefaultRevocationTimeout = 30 * time.Second

// ErrKeyPermanentlyDeleted is returned by UnrevokeKey when the CMK's scheduled
// deletion has already completed (DescribeKey → NotFoundException): AWS has
// destroyed the key material and the revocation can no longer be undone. It is a
// specialization of encryption.ErrKeyRevoked — a KeyPermanentlyDeletedError
// matches both with errors.Is — so callers that already classify revoked keys keep
// working while those that need to tell "recoverable" from "gone" can.
var ErrKeyPermanentlyDeleted = errors.New("mink/kms: key has been deleted; revocation is permanent")

// ErrAliasNotFound is returned by the revocation probes (IsRevoked, RevocationState,
// RevokeKey, UnrevokeKey and the decrypt-path probe) when keyID is an alias
// ("alias/...") that DescribeKey cannot resolve. An alias is a mutable pointer, not
// key material: a deleted or re-pointed alias says nothing about whether the key the
// data was sealed under still exists, so a missing alias is reported as an UNKNOWN
// state (an error) rather than as Revoked — otherwise deleting an alias would let
// erasure verification certify a crypto-shred that never happened. Stamp key ARNs
// (or bare key ids) so revocation state is always answered for the sealing key.
var ErrAliasNotFound = errors.New("mink/kms: key alias not found; revocation state unknown")

// isAliasID reports whether keyID names a KMS alias rather than a key: either the
// bare "alias/<name>" form or an alias ARN ("arn:aws:kms:...:alias/<name>").
func isAliasID(keyID string) bool {
	return strings.HasPrefix(keyID, "alias/") || strings.Contains(keyID, ":alias/")
}

// KeyPermanentlyDeletedError is the typed form of ErrKeyPermanentlyDeleted,
// carrying the key id the caller supplied. errors.Is matches
// ErrKeyPermanentlyDeleted and encryption.ErrKeyRevoked; Unwrap yields
// ErrKeyPermanentlyDeleted.
type KeyPermanentlyDeletedError struct {
	KeyID string
}

// Error returns the error message.
func (e *KeyPermanentlyDeletedError) Error() string {
	return fmt.Sprintf("mink/kms: key %q has been deleted; revocation is permanent", e.KeyID)
}

// Is reports whether this error matches the target error.
func (e *KeyPermanentlyDeletedError) Is(target error) bool {
	return target == ErrKeyPermanentlyDeleted || target == encryption.ErrKeyRevoked
}

// Unwrap returns the sentinel for errors.Unwrap().
func (e *KeyPermanentlyDeletedError) Unwrap() error {
	return ErrKeyPermanentlyDeleted
}

// Pending-deletion window bounds accepted by AWS KMS ScheduleKeyDeletion.
const (
	minPendingWindowDays int32 = 7
	maxPendingWindowDays int32 = 30
)

// KMSClient defines the subset of the KMS API used by the provider.
type KMSClient interface {
	Encrypt(ctx context.Context, params *kms.EncryptInput, optFns ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(ctx context.Context, params *kms.DecryptInput, optFns ...func(*kms.Options)) (*kms.DecryptOutput, error)
	GenerateDataKey(ctx context.Context, params *kms.GenerateDataKeyInput, optFns ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error)
}

// KMSRevocationClient is an OPTIONAL extension of KMSClient. When the injected
// client also implements it, the provider implements encryption.Revocable and
// supports crypto-shredding (GDPR erasure) by scheduling deletion of the customer
// master key (CMK). A CMK pending deletion is immediately unusable for decrypt,
// and AWS destroys the key material permanently after the pending window.
//
// DescribeKey is also used to resolve the id the caller supplied (which may be an
// alias or ARN) to the canonical KeyMetadata.KeyId before any mutating call, and
// to read the key state. (*kms.Client) satisfies this interface.
type KMSRevocationClient interface {
	ScheduleKeyDeletion(ctx context.Context, params *kms.ScheduleKeyDeletionInput, optFns ...func(*kms.Options)) (*kms.ScheduleKeyDeletionOutput, error)
	DescribeKey(ctx context.Context, params *kms.DescribeKeyInput, optFns ...func(*kms.Options)) (*kms.DescribeKeyOutput, error)
}

// KMSDeletionCanceller is an OPTIONAL extension of KMSRevocationClient. When the
// injected client also implements it, UnrevokeKey can restore a CMK that is still
// inside its pending-deletion window (CancelKeyDeletion leaves the key Disabled,
// so EnableKey follows). Without it UnrevokeKey returns
// encryption.ErrRevocationUnsupported. (*kms.Client) satisfies this interface.
type KMSDeletionCanceller interface {
	CancelKeyDeletion(ctx context.Context, params *kms.CancelKeyDeletionInput, optFns ...func(*kms.Options)) (*kms.CancelKeyDeletionOutput, error)
	EnableKey(ctx context.Context, params *kms.EnableKeyInput, optFns ...func(*kms.Options)) (*kms.EnableKeyOutput, error)
}

// Compile-time interface checks. The revocation assertions hold at the type level;
// the methods return ErrRevocationUnsupported unless the injected client also
// implements KMSRevocationClient (and KMSDeletionCanceller for UnrevokeKey).
var (
	_ encryption.Provider             = (*Provider)(nil)
	_ encryption.Revocable            = (*Provider)(nil)
	_ encryption.StatefulRevocable    = (*Provider)(nil)
	_ encryption.RecoverableRevocable = (*Provider)(nil)
)

// Provider implements encryption.Provider using AWS KMS.
//
// Revocation semantics (encryption.Revocable / StatefulRevocable /
// RecoverableRevocable): a CMK pending deletion blocks decryption immediately —
// IsRevoked reports true and Decrypt surfaces ErrKeyRevoked — but AWS keeps the key
// material recoverable via CancelKeyDeletion for the 7–30 day pending window, so
// RevocationState reports it as encryption.SoftRevoked. Only once AWS has finished
// the deletion (DescribeKey → NotFoundException) is the key reported as
// encryption.Revoked, which is what erasure verification needs before it certifies
// data as permanently erased. A merely Disabled CMK is fully reversible (EnableKey)
// and is NotRevoked.
type Provider struct {
	client            KMSClient
	mu                sync.RWMutex
	closed            bool
	pendingWindowDays int32
	revocationTimeout time.Duration
}

// Option configures a KMS Provider.
type Option func(*Provider)

// WithKMSClient sets the KMS client.
func WithKMSClient(client KMSClient) Option {
	return func(p *Provider) {
		p.client = client
	}
}

// WithPendingDeletionWindow sets the AWS KMS pending-deletion window in days used by
// RevokeKey. AWS only accepts 7–30 days, so an out-of-range value is clamped into that
// range — a misconfigured window cannot fail at RevokeKey time (the erasure moment).
// Defaults to 7 (the AWS minimum) when unset.
func WithPendingDeletionWindow(days int32) Option {
	return func(p *Provider) {
		p.pendingWindowDays = clampWindowDays(days)
	}
}

// WithRevocationTimeout bounds every KMS call made by the context-free revocation
// methods (RevokeKey, IsRevoked, RevocationState, SoftRevokeKey, UnrevokeKey),
// which have no caller context to inherit a deadline from. It also caps the
// revocation probe run on the Decrypt/DecryptDataKey error path when the caller's
// own context carries no deadline. Non-positive values are ignored; the default is
// DefaultRevocationTimeout (30s).
func WithRevocationTimeout(d time.Duration) Option {
	return func(p *Provider) {
		if d > 0 {
			p.revocationTimeout = d
		}
	}
}

// New creates a new KMS encryption provider.
func New(opts ...Option) *Provider {
	p := &Provider{revocationTimeout: DefaultRevocationTimeout}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Encrypt encrypts plaintext using the KMS key.
func (p *Provider) Encrypt(ctx context.Context, keyID string, plaintext []byte) ([]byte, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}

	output, err := p.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     &keyID,
		Plaintext: plaintext,
	})
	if err != nil {
		return nil, encryption.NewEncryptionError(keyID, "", fmt.Errorf("KMS encrypt: %w", err))
	}
	return output.CiphertextBlob, nil
}

// decryptError maps a decrypt failure to ErrKeyRevoked when keyID has been revoked
// (crypto-shredded) — AWS rejects operations on a key pending deletion or already
// deleted — so a WithDecryptionErrorHandler checking for ErrKeyRevoked recognizes
// it as shredded. Otherwise it is a genuine ErrDecryptionFailed. The revocation
// probe runs only on the (rare) error path and under the caller's ctx, so the
// caller's cancellation, deadline and values (tracing) propagate to the DescribeKey
// call; a ctx with no deadline is bounded by the revocation timeout. A probe that
// cannot determine the key state (cancelled ctx, KMS unreachable) fails closed as a
// decryption error — never as a confirmed revocation.
func (p *Provider) decryptError(ctx context.Context, keyID string, cause error) error {
	if rc, ok := p.client.(KMSRevocationClient); ok {
		probeCtx, cancel := p.boundedContext(ctx)
		defer cancel()
		if info, err := p.describe(probeCtx, rc, keyID); err == nil && info.state != encryption.NotRevoked {
			return encryption.NewKeyRevokedError(keyID)
		}
	}
	return encryption.NewDecryptionError(keyID, "", cause)
}

// Decrypt decrypts ciphertext using the KMS key.
func (p *Provider) Decrypt(ctx context.Context, keyID string, ciphertext []byte) ([]byte, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}

	output, err := p.client.Decrypt(ctx, &kms.DecryptInput{
		KeyId:          &keyID,
		CiphertextBlob: ciphertext,
	})
	if err != nil {
		return nil, p.decryptError(ctx, keyID, fmt.Errorf("KMS decrypt: %w", err))
	}
	return output.Plaintext, nil
}

// GenerateDataKey creates a new DEK using KMS GenerateDataKey API.
// Returns a 256-bit (32-byte) AES key.
func (p *Provider) GenerateDataKey(ctx context.Context, keyID string) (*encryption.DataKey, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}

	output, err := p.client.GenerateDataKey(ctx, &kms.GenerateDataKeyInput{
		KeyId:   &keyID,
		KeySpec: types.DataKeySpecAes256,
	})
	if err != nil {
		return nil, encryption.NewEncryptionError(keyID, "", fmt.Errorf("KMS generate data key: %w", err))
	}

	return &encryption.DataKey{
		Plaintext:  output.Plaintext,
		Ciphertext: output.CiphertextBlob,
		KeyID:      keyID,
	}, nil
}

// DecryptDataKey decrypts an encrypted DEK using the KMS Decrypt API.
func (p *Provider) DecryptDataKey(ctx context.Context, keyID string, encryptedKey []byte) ([]byte, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}

	output, err := p.client.Decrypt(ctx, &kms.DecryptInput{
		KeyId:          &keyID,
		CiphertextBlob: encryptedKey,
	})
	if err != nil {
		return nil, p.decryptError(ctx, keyID, fmt.Errorf("KMS decrypt data key: %w", err))
	}
	return output.Plaintext, nil
}

// RevokeKey crypto-shreds keyID by scheduling deletion of its KMS CMK with the
// configured pending window (WithPendingDeletionWindow, default 7 days). It
// implements encryption.Revocable. It requires the injected client to implement
// KMSRevocationClient, otherwise it returns ErrRevocationUnsupported. It is
// idempotent: a CMK already pending deletion, or already deleted, returns nil. A
// merely-disabled CMK is reversible (EnableKey), so it is NOT treated as already
// revoked — RevokeKey schedules its deletion so the crypto-shred is actually
// permanent.
//
// keyID may be a key id, ARN or alias; it is resolved through DescribeKey and the
// canonical KeyMetadata.KeyId is what ScheduleKeyDeletion receives (it rejects
// aliases). An alias is resolved AT CALL TIME: RevokeKey("alias/x") shreds whatever
// key the alias points at when the call runs, and if the alias is later re-pointed
// (or deleted), IsRevoked("alias/x") answers for the new target (or reports the
// missing alias as revoked), not for the key the data was sealed under. Stamp and
// revoke key ARNs (see WithDefaultKeyID) so the id in event metadata is immutable.
// The call is bounded by the revocation timeout. Note that AWS keeps the key
// recoverable for the pending window: RevocationState reports SoftRevoked until
// the deletion completes.
func (p *Provider) RevokeKey(keyID string) error {
	return p.scheduleDeletion(keyID, p.defaultWindowDays())
}

// SoftRevokeKey schedules deletion of keyID's CMK with a pending window derived
// from graceWindow, during which UnrevokeKey can restore it. It implements
// encryption.RecoverableRevocable. graceWindow is rounded up to whole days and
// clamped to the 7–30 days AWS accepts; a non-positive window uses the configured
// default (WithPendingDeletionWindow). On KMS there is no separate soft-revoke
// primitive — every scheduled deletion is recoverable until AWS completes it — so
// a key already pending deletion keeps its existing window (idempotent, nil).
func (p *Provider) SoftRevokeKey(keyID string, graceWindow time.Duration) error {
	return p.scheduleDeletion(keyID, p.windowDays(graceWindow))
}

// UnrevokeKey restores a CMK that is still pending deletion by cancelling the
// deletion and re-enabling the key (CancelKeyDeletion leaves it Disabled). It
// implements encryption.RecoverableRevocable and requires the injected client to
// implement KMSDeletionCanceller, otherwise it returns ErrRevocationUnsupported.
//
// It is idempotent and retryable, driven by the key's current state:
//
//   - PendingDeletion → CancelKeyDeletion, then EnableKey.
//   - Disabled (not pending) → EnableKey only. This is the state a previous
//     UnrevokeKey leaves behind when CancelKeyDeletion succeeded but EnableKey
//     failed, so simply calling UnrevokeKey again completes the restore.
//   - Enabled (or any other state) → nothing to undo; returns nil.
//   - Already deleted (DescribeKey → NotFound) → the key material is gone and the
//     revocation is permanent: returns a *KeyPermanentlyDeletedError (errors.Is
//     matches ErrKeyPermanentlyDeleted and encryption.ErrKeyRevoked).
//
// The id is resolved through DescribeKey like RevokeKey (an alias names whatever
// key it points at when the call runs). The call is bounded by the revocation
// timeout.
func (p *Provider) UnrevokeKey(keyID string) error {
	rc, err := p.revocationClient()
	if err != nil {
		return err
	}
	dc, ok := p.client.(KMSDeletionCanceller)
	if !ok {
		return encryption.ErrRevocationUnsupported
	}
	ctx, cancel := p.adminContext()
	defer cancel()

	info, err := p.describe(ctx, rc, keyID)
	if err != nil {
		return err
	}
	switch {
	case info.state == encryption.Revoked:
		return &KeyPermanentlyDeletedError{KeyID: keyID}
	case info.state == encryption.SoftRevoked:
		if _, err := dc.CancelKeyDeletion(ctx, &kms.CancelKeyDeletionInput{KeyId: &info.id}); err != nil {
			return encryption.NewEncryptionError(keyID, "", fmt.Errorf("KMS cancel key deletion: %w", err))
		}
	case info.keyState == types.KeyStateDisabled:
		// A Disabled-but-not-pending key is what a half-finished UnrevokeKey leaves
		// behind (CancelKeyDeletion done, EnableKey failed): finish the restore.
	default:
		return nil // Enabled or otherwise not revoked: nothing to undo
	}
	if _, err := dc.EnableKey(ctx, &kms.EnableKeyInput{KeyId: &info.id}); err != nil {
		return encryption.NewEncryptionError(keyID, "", fmt.Errorf("KMS enable key: %w", err))
	}
	return nil
}

// IsRevoked reports whether decryption under keyID is blocked: its CMK is pending
// deletion or already deleted (NotFound). A merely-disabled CMK is reversible via
// EnableKey, so it is NOT reported as revoked. It implements encryption.Revocable.
//
// A key pending deletion is reported as revoked even though AWS can still restore
// it (CancelKeyDeletion): decryption is already refused, and Decrypt must surface
// ErrKeyRevoked. Use RevocationState (or encryption.GetRevocationState, which
// prefers it) to tell that still-recoverable state from a completed deletion.
//
// keyID is resolved through DescribeKey at call time. For an alias the answer
// describes the key the alias points at NOW: re-pointing the alias after an
// erasure changes what IsRevoked reports, and a deleted alias is indistinguishable
// from a deleted key (both are NotFound → revoked). Erasure verification should
// therefore be run against the key ARNs stamped in event metadata, never against
// alias names (see WithDefaultKeyID).
func (p *Provider) IsRevoked(keyID string) (bool, error) {
	state, err := p.RevocationState(keyID)
	if err != nil {
		return false, err
	}
	return state != encryption.NotRevoked, nil
}

// RevocationState reports the fine-grained revocation state of keyID's CMK. It
// implements encryption.StatefulRevocable:
//
//   - KeyStatePendingDeletion → encryption.SoftRevoked: decryption is blocked, but
//     CancelKeyDeletion (UnrevokeKey) can still restore the key until the pending
//     window elapses, so the data is NOT yet permanently erased.
//   - DescribeKey NotFoundException (deletion completed) → encryption.Revoked: the
//     key material is gone and the data is permanently unrecoverable.
//   - Any other state (Enabled, Disabled, PendingImport, ...) → encryption.NotRevoked.
//
// A DescribeKey failure other than NotFound is surfaced as an error (wrapping
// encryption.ErrDecryptionFailed), never guessed.
func (p *Provider) RevocationState(keyID string) (encryption.RevocationState, error) {
	rc, err := p.revocationClient()
	if err != nil {
		return encryption.NotRevoked, err
	}
	ctx, cancel := p.adminContext()
	defer cancel()
	info, err := p.describe(ctx, rc, keyID)
	if err != nil {
		return encryption.NotRevoked, err
	}
	return info.state, nil
}

// scheduleDeletion is the shared body of RevokeKey and SoftRevokeKey.
func (p *Provider) scheduleDeletion(keyID string, days int32) error {
	rc, err := p.revocationClient()
	if err != nil {
		return err
	}
	ctx, cancel := p.adminContext()
	defer cancel()

	info, err := p.describe(ctx, rc, keyID)
	if err != nil {
		return err
	}
	if info.state != encryption.NotRevoked {
		return nil // idempotent: already pending deletion or already deleted
	}
	if _, err := rc.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{
		KeyId:               &info.id,
		PendingWindowInDays: &days,
	}); err != nil {
		return encryption.NewEncryptionError(keyID, "", fmt.Errorf("KMS schedule key deletion: %w", err))
	}
	return nil
}

// keyInfo is what describe learns about a CMK: its canonical id, the revocation
// state derived from its KMS key state, and the raw KMS key state itself (empty
// when the key no longer exists), which UnrevokeKey needs to tell a Disabled key
// — the state a half-finished UnrevokeKey leaves behind — from an Enabled one.
type keyInfo struct {
	id       string
	state    encryption.RevocationState
	keyState types.KeyState
}

// describe resolves keyID (id, ARN or alias) through DescribeKey to the canonical
// KeyMetadata.KeyId — the only form ScheduleKeyDeletion / CancelKeyDeletion accept,
// and the key the alias pointed at when the probe ran — and maps the key state to
// a RevocationState. A CMK whose deletion has completed no longer exists, so
// DescribeKey returns NotFound: for a key id or ARN that is the terminal crypto-shred
// state and is reported as Revoked, not as an error, so Verify and decryptError keep
// recognizing a permanently erased key after AWS finishes the deletion. For an ALIAS
// a NotFound only proves the alias is gone, not the key, so it is surfaced as
// ErrAliasNotFound (state unknown). Any other failure is surfaced so the caller never
// acts on a guessed state.
func (p *Provider) describe(ctx context.Context, rc KMSRevocationClient, keyID string) (keyInfo, error) {
	out, err := rc.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: &keyID})
	if err != nil {
		var notFound *types.NotFoundException
		if errors.As(err, &notFound) {
			if isAliasID(keyID) {
				return keyInfo{}, encryption.NewDecryptionError(keyID, "", fmt.Errorf("%w: %q", ErrAliasNotFound, keyID))
			}
			return keyInfo{id: keyID, state: encryption.Revoked}, nil
		}
		return keyInfo{}, encryption.NewDecryptionError(keyID, "", fmt.Errorf("KMS describe key: %w", err))
	}
	info := keyInfo{id: keyID, state: encryption.NotRevoked}
	if out == nil || out.KeyMetadata == nil {
		return info, nil
	}
	if out.KeyMetadata.KeyId != nil && *out.KeyMetadata.KeyId != "" {
		info.id = *out.KeyMetadata.KeyId
	}
	info.keyState = out.KeyMetadata.KeyState
	// Only PendingDeletion blocks decryption. A merely Disabled CMK is fully
	// reversible via EnableKey, so treating it as revoked would let RevokeKey skip
	// scheduling deletion and make Verify certify an erasure whose data is still
	// recoverable; RevokeKey schedules deletion for a disabled key instead.
	if out.KeyMetadata.KeyState == types.KeyStatePendingDeletion {
		info.state = encryption.SoftRevoked
	}
	return info, nil
}

// revocationClient returns the injected client as a KMSRevocationClient, or
// ErrRevocationUnsupported if it does not support revocation.
func (p *Provider) revocationClient() (KMSRevocationClient, error) {
	if err := p.checkClosed(); err != nil {
		return nil, err
	}
	rc, ok := p.client.(KMSRevocationClient)
	if !ok {
		return nil, encryption.ErrRevocationUnsupported
	}
	return rc, nil
}

// timeout returns the effective revocation timeout.
func (p *Provider) timeout() time.Duration {
	if p.revocationTimeout > 0 {
		return p.revocationTimeout
	}
	return DefaultRevocationTimeout
}

// adminContext returns the context used by the context-free revocation methods:
// a background context bounded by the revocation timeout.
func (p *Provider) adminContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), p.timeout())
}

// boundedContext returns ctx itself when it already carries a deadline (the
// caller's own budget is respected, never shortened or extended) and otherwise
// derives a child bounded by the revocation timeout. Either way the caller's
// cancellation and values propagate.
func (p *Provider) boundedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, p.timeout())
}

// defaultWindowDays is the pending-deletion window RevokeKey uses.
func (p *Provider) defaultWindowDays() int32 {
	if p.pendingWindowDays == 0 {
		return minPendingWindowDays
	}
	return p.pendingWindowDays
}

// windowDays converts a SoftRevokeKey grace window to whole days (rounded up) and
// clamps it to the AWS-accepted range. Non-positive durations use the default.
func (p *Provider) windowDays(graceWindow time.Duration) int32 {
	if graceWindow <= 0 {
		return p.defaultWindowDays()
	}
	days := math.Ceil(graceWindow.Hours() / 24)
	if days > float64(maxPendingWindowDays) {
		return maxPendingWindowDays
	}
	if days < float64(minPendingWindowDays) {
		return minPendingWindowDays
	}
	return int32(days)
}

// clampWindowDays clamps days into the 7–30 range AWS accepts.
func clampWindowDays(days int32) int32 {
	if days < minPendingWindowDays {
		return minPendingWindowDays
	}
	if days > maxPendingWindowDays {
		return maxPendingWindowDays
	}
	return days
}

// Close marks the provider as closed.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *Provider) checkClosed() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return encryption.ErrProviderClosed
	}
	if p.client == nil {
		return encryption.NewEncryptionError("", "", fmt.Errorf("kms client not configured: use WithKMSClient option"))
	}
	return nil
}
