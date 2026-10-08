package kms

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/encryption"
)

// mockKMSRecoverableClient adds the optional KMSDeletionCanceller API so the
// provider can undo a pending deletion.
type mockKMSRecoverableClient struct {
	*mockKMSRevocationClient
	cancelled []string
	enabled   []string
	cancelErr error
	enableErr error
}

func (m *mockKMSRecoverableClient) CancelKeyDeletion(_ context.Context, params *kms.CancelKeyDeletionInput, _ ...func(*kms.Options)) (*kms.CancelKeyDeletionOutput, error) {
	if m.cancelErr != nil {
		return nil, m.cancelErr
	}
	m.cancelled = append(m.cancelled, derefString(params.KeyId))
	m.state = types.KeyStateDisabled // AWS leaves a key Disabled after cancelling its deletion
	return &kms.CancelKeyDeletionOutput{}, nil
}

func (m *mockKMSRecoverableClient) EnableKey(_ context.Context, params *kms.EnableKeyInput, _ ...func(*kms.Options)) (*kms.EnableKeyOutput, error) {
	if m.enableErr != nil {
		return nil, m.enableErr
	}
	m.enabled = append(m.enabled, derefString(params.KeyId))
	m.state = types.KeyStateEnabled
	return &kms.EnableKeyOutput{}, nil
}

// failingScheduleClient makes ScheduleKeyDeletion fail.
type failingScheduleClient struct {
	*mockKMSRevocationClient
}

func (f *failingScheduleClient) ScheduleKeyDeletion(context.Context, *kms.ScheduleKeyDeletionInput, ...func(*kms.Options)) (*kms.ScheduleKeyDeletionOutput, error) {
	return nil, errors.New("kms: access denied")
}

func newRevocationMock() *mockKMSRevocationClient {
	return &mockKMSRevocationClient{mockKMSClient: &mockKMSClient{}}
}

func newRecoverableMock() *mockKMSRecoverableClient {
	return &mockKMSRecoverableClient{mockKMSRevocationClient: newRevocationMock()}
}

// --- StatefulRevocable: PendingDeletion is recoverable, NotFound is permanent ---

func TestProvider_RevocationState_MapsKMSKeyStates(t *testing.T) {
	tests := []struct {
		name        string
		state       types.KeyState
		notFound    bool
		wantState   encryption.RevocationState
		wantRevoked bool
	}{
		{"enabled", types.KeyStateEnabled, false, encryption.NotRevoked, false},
		{"disabled is reversible", types.KeyStateDisabled, false, encryption.NotRevoked, false},
		{"pending import", types.KeyStatePendingImport, false, encryption.NotRevoked, false},
		{"pending deletion is still recoverable", types.KeyStatePendingDeletion, false, encryption.SoftRevoked, true},
		{"deleted (NotFound) is permanent", "", true, encryption.Revoked, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := newRevocationMock()
			rc.state, rc.notFound = tt.state, tt.notFound
			p := New(WithKMSClient(rc))
			defer func() { _ = p.Close() }()

			state, err := p.RevocationState("k")
			require.NoError(t, err)
			assert.Equal(t, tt.wantState, state)

			revoked, err := p.IsRevoked("k")
			require.NoError(t, err)
			assert.Equal(t, tt.wantRevoked, revoked, "IsRevoked stays true while pending deletion: decryption is blocked")
		})
	}
}

func TestProvider_RevocationState_GetRevocationStatePrefersStateful(t *testing.T) {
	// Before the provider implemented StatefulRevocable, GetRevocationState fell
	// back to IsRevoked and reported a key pending deletion as permanently Revoked,
	// letting erasure verification certify data that CancelKeyDeletion can still
	// restore for up to 30 days.
	rc := newRevocationMock()
	rc.state = types.KeyStatePendingDeletion
	p := New(WithKMSClient(rc))
	defer func() { _ = p.Close() }()

	state, err := encryption.GetRevocationState(p, "k")
	require.NoError(t, err)
	assert.Equal(t, encryption.SoftRevoked, state)

	rc.notFound = true // AWS finished the deletion
	state, err = encryption.GetRevocationState(p, "k")
	require.NoError(t, err)
	assert.Equal(t, encryption.Revoked, state)
}

func TestProvider_RevocationState_Errors(t *testing.T) {
	t.Run("client without revocation support", func(t *testing.T) {
		p := New(WithKMSClient(&mockKMSClient{}))
		defer func() { _ = p.Close() }()
		_, err := p.RevocationState("k")
		assert.ErrorIs(t, err, encryption.ErrRevocationUnsupported)
	})
	t.Run("closed provider", func(t *testing.T) {
		p := New(WithKMSClient(newRevocationMock()))
		require.NoError(t, p.Close())
		_, err := p.RevocationState("k")
		assert.ErrorIs(t, err, encryption.ErrProviderClosed)
	})
	t.Run("describe failure is surfaced, never guessed", func(t *testing.T) {
		rc := newRevocationMock()
		rc.describeFunc = func(context.Context, *kms.DescribeKeyInput) (*kms.DescribeKeyOutput, error) {
			return nil, errors.New("access denied")
		}
		p := New(WithKMSClient(rc))
		defer func() { _ = p.Close() }()
		_, err := p.RevocationState("k")
		require.Error(t, err)
		assert.ErrorIs(t, err, encryption.ErrDecryptionFailed)
		assert.Contains(t, err.Error(), "access denied")
		// With the key state unknown, RevokeKey must not schedule anything.
		assert.Error(t, p.RevokeKey("k"))
		assert.Equal(t, 0, rc.scheduled)
	})
	t.Run("nil KeyMetadata is not revoked", func(t *testing.T) {
		rc := newRevocationMock()
		rc.describeFunc = func(context.Context, *kms.DescribeKeyInput) (*kms.DescribeKeyOutput, error) {
			return &kms.DescribeKeyOutput{}, nil
		}
		p := New(WithKMSClient(rc))
		defer func() { _ = p.Close() }()
		state, err := p.RevocationState("k")
		require.NoError(t, err)
		assert.Equal(t, encryption.NotRevoked, state)
	})
}

func TestProvider_PendingDeletion_DecryptStillMapsToErrKeyRevoked(t *testing.T) {
	rc := newRevocationMock()
	rc.state = types.KeyStatePendingDeletion
	p := New(WithKMSClient(rc))
	defer func() { _ = p.Close() }()
	ctx := context.Background()

	_, err := p.Decrypt(ctx, "k", []byte("enc:x"))
	assert.ErrorIs(t, err, encryption.ErrKeyRevoked)
	_, err = p.DecryptDataKey(ctx, "k", []byte("enc:x"))
	assert.ErrorIs(t, err, encryption.ErrKeyRevoked)

	rc.notFound = true // deletion completed
	_, err = p.Decrypt(ctx, "k", []byte("enc:x"))
	assert.ErrorIs(t, err, encryption.ErrKeyRevoked)
}

// --- Alias / ARN resolution ---

func TestProvider_RevokeKey_ResolvesAliasToCanonicalKeyID(t *testing.T) {
	rc := newRevocationMock()
	rc.canonicalID = "1234abcd-12ab-34cd-56ef-1234567890ab"
	p := New(WithKMSClient(rc))
	defer func() { _ = p.Close() }()

	require.NoError(t, p.RevokeKey("alias/customer-42"))
	assert.Equal(t, []string{rc.canonicalID}, rc.scheduledIDs,
		"ScheduleKeyDeletion rejects aliases: the DescribeKey-resolved key id must be used")

	revoked, err := p.IsRevoked("alias/customer-42")
	require.NoError(t, err)
	assert.True(t, revoked)
}

func TestProvider_RevokeKey_FallsBackToInputIDWhenKMSOmitsKeyId(t *testing.T) {
	rc := newRevocationMock()
	rc.describeFunc = func(context.Context, *kms.DescribeKeyInput) (*kms.DescribeKeyOutput, error) {
		return &kms.DescribeKeyOutput{KeyMetadata: &types.KeyMetadata{KeyState: types.KeyStateEnabled}}, nil
	}
	p := New(WithKMSClient(rc))
	defer func() { _ = p.Close() }()

	require.NoError(t, p.RevokeKey("k"))
	assert.Equal(t, []string{"k"}, rc.scheduledIDs)
}

func TestProvider_RevokeKey_ScheduleError(t *testing.T) {
	p := New(WithKMSClient(&failingScheduleClient{mockKMSRevocationClient: newRevocationMock()}))
	defer func() { _ = p.Close() }()

	err := p.RevokeKey("k")
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
	assert.Contains(t, err.Error(), "schedule key deletion")
}

// --- SoftRevokeKey (RecoverableRevocable) ---

func TestProvider_SoftRevokeKey_WindowRoundedAndClamped(t *testing.T) {
	tests := []struct {
		name   string
		opts   []Option
		window time.Duration
		want   int32
	}{
		{"exact days", nil, 10 * 24 * time.Hour, 10},
		{"rounds partial days up", nil, 10*24*time.Hour + time.Second, 11},
		{"below the AWS minimum clamps to 7", nil, 36 * time.Hour, 7},
		{"above the AWS maximum clamps to 30", nil, 60 * 24 * time.Hour, 30},
		{"zero uses the provider default", nil, 0, 7},
		{"zero uses the configured default", []Option{WithPendingDeletionWindow(14)}, 0, 14},
		{"negative uses the configured default", []Option{WithPendingDeletionWindow(14)}, -time.Hour, 14},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := newRevocationMock()
			p := New(append([]Option{WithKMSClient(rc)}, tt.opts...)...)
			defer func() { _ = p.Close() }()

			require.NoError(t, p.SoftRevokeKey("k", tt.window))
			require.Equal(t, []int32{tt.want}, rc.windows)
			state, err := p.RevocationState("k")
			require.NoError(t, err)
			assert.Equal(t, encryption.SoftRevoked, state)
		})
	}
}

func TestProvider_SoftRevokeKey_IdempotentAndUnsupported(t *testing.T) {
	t.Run("already pending deletion keeps its window", func(t *testing.T) {
		rc := newRevocationMock()
		rc.state = types.KeyStatePendingDeletion
		p := New(WithKMSClient(rc))
		defer func() { _ = p.Close() }()
		require.NoError(t, encryption.SoftRevoke(p, "k", 30*24*time.Hour))
		assert.Equal(t, 0, rc.scheduled)
	})
	t.Run("already deleted", func(t *testing.T) {
		rc := newRevocationMock()
		rc.notFound = true
		p := New(WithKMSClient(rc))
		defer func() { _ = p.Close() }()
		require.NoError(t, p.SoftRevokeKey("k", 7*24*time.Hour))
		assert.Equal(t, 0, rc.scheduled)
	})
	t.Run("client without revocation support", func(t *testing.T) {
		p := New(WithKMSClient(&mockKMSClient{}))
		defer func() { _ = p.Close() }()
		assert.ErrorIs(t, p.SoftRevokeKey("k", 7*24*time.Hour), encryption.ErrRevocationUnsupported)
	})
}

// --- UnrevokeKey (RecoverableRevocable via KMSDeletionCanceller) ---

func TestProvider_UnrevokeKey_RestoresKeyPendingDeletion(t *testing.T) {
	rc := newRecoverableMock()
	p := New(WithKMSClient(rc))
	defer func() { _ = p.Close() }()
	ctx := context.Background()

	ct, err := p.Encrypt(ctx, "k", []byte("secret"))
	require.NoError(t, err)

	require.NoError(t, p.SoftRevokeKey("k", 7*24*time.Hour))
	_, err = p.Decrypt(ctx, "k", ct)
	require.ErrorIs(t, err, encryption.ErrKeyRevoked)
	state, err := p.RevocationState("k")
	require.NoError(t, err)
	require.Equal(t, encryption.SoftRevoked, state)

	require.NoError(t, encryption.Unrevoke(p, "k"))
	assert.Equal(t, []string{"k"}, rc.cancelled)
	assert.Equal(t, []string{"k"}, rc.enabled, "CancelKeyDeletion leaves the key Disabled; EnableKey must follow")

	state, err = p.RevocationState("k")
	require.NoError(t, err)
	assert.Equal(t, encryption.NotRevoked, state)
	pt, err := p.Decrypt(ctx, "k", ct)
	require.NoError(t, err)
	assert.Equal(t, "secret", string(pt))
}

func TestProvider_UnrevokeKey_ResolvesAlias(t *testing.T) {
	rc := newRecoverableMock()
	rc.state = types.KeyStatePendingDeletion
	rc.canonicalID = "1234abcd-12ab-34cd-56ef-1234567890ab"
	p := New(WithKMSClient(rc))
	defer func() { _ = p.Close() }()

	require.NoError(t, p.UnrevokeKey("alias/customer-42"))
	assert.Equal(t, []string{rc.canonicalID}, rc.cancelled)
	assert.Equal(t, []string{rc.canonicalID}, rc.enabled)
}

func TestProvider_UnrevokeKey_NotPendingIsNoOp(t *testing.T) {
	for _, st := range []types.KeyState{types.KeyStateEnabled, types.KeyStatePendingImport, types.KeyStateUnavailable} {
		t.Run(string(st), func(t *testing.T) {
			rc := newRecoverableMock()
			rc.state = st
			p := New(WithKMSClient(rc))
			defer func() { _ = p.Close() }()

			require.NoError(t, p.UnrevokeKey("k"))
			assert.Empty(t, rc.cancelled)
			assert.Empty(t, rc.enabled)
		})
	}
}

// A Disabled-but-not-pending key is the state a half-finished UnrevokeKey leaves
// behind (CancelKeyDeletion succeeded, EnableKey failed): UnrevokeKey completes the
// restore with EnableKey alone, never calling CancelKeyDeletion on a key that is
// not pending deletion (AWS would reject it).
func TestProvider_UnrevokeKey_DisabledKeyIsEnabledOnly(t *testing.T) {
	rc := newRecoverableMock()
	rc.state = types.KeyStateDisabled
	rc.canonicalID = "1234abcd-12ab-34cd-56ef-1234567890ab"
	p := New(WithKMSClient(rc))
	defer func() { _ = p.Close() }()

	require.NoError(t, p.UnrevokeKey("alias/customer-42"))
	assert.Empty(t, rc.cancelled, "a key that is not pending deletion must not be 'cancelled'")
	assert.Equal(t, []string{rc.canonicalID}, rc.enabled)
	assert.Equal(t, types.KeyStateEnabled, rc.state)

	// Idempotent: a second call finds the key Enabled and does nothing.
	require.NoError(t, p.UnrevokeKey("alias/customer-42"))
	assert.Len(t, rc.enabled, 1)
}

func TestProvider_UnrevokeKey_DeletedKeyIsPermanent(t *testing.T) {
	rc := newRecoverableMock()
	rc.notFound = true
	p := New(WithKMSClient(rc))
	defer func() { _ = p.Close() }()

	err := p.UnrevokeKey("k")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permanent")
	assert.ErrorIs(t, err, ErrKeyPermanentlyDeleted, "typed/sentinel: callers can classify the permanent case")
	assert.ErrorIs(t, err, encryption.ErrKeyRevoked, "a permanently deleted key is a revoked key")
	assert.NotErrorIs(t, err, encryption.ErrEncryptionFailed)
	var pd *KeyPermanentlyDeletedError
	require.True(t, errors.As(err, &pd))
	assert.Equal(t, "k", pd.KeyID)
	assert.Equal(t, ErrKeyPermanentlyDeleted, errors.Unwrap(pd))
	assert.Empty(t, rc.cancelled)
	assert.Empty(t, rc.enabled)
}

func TestProvider_UnrevokeKey_Unsupported(t *testing.T) {
	t.Run("client without KMSDeletionCanceller", func(t *testing.T) {
		rc := newRevocationMock()
		rc.state = types.KeyStatePendingDeletion
		p := New(WithKMSClient(rc))
		defer func() { _ = p.Close() }()
		assert.ErrorIs(t, p.UnrevokeKey("k"), encryption.ErrRevocationUnsupported)
		assert.ErrorIs(t, encryption.Unrevoke(p, "k"), encryption.ErrRevocationUnsupported)
	})
	t.Run("client without revocation support", func(t *testing.T) {
		p := New(WithKMSClient(&mockKMSClient{}))
		defer func() { _ = p.Close() }()
		assert.ErrorIs(t, p.UnrevokeKey("k"), encryption.ErrRevocationUnsupported)
	})
	t.Run("closed provider", func(t *testing.T) {
		p := New(WithKMSClient(newRecoverableMock()))
		require.NoError(t, p.Close())
		assert.ErrorIs(t, p.UnrevokeKey("k"), encryption.ErrProviderClosed)
	})
}

func TestProvider_UnrevokeKey_AWSErrors(t *testing.T) {
	t.Run("CancelKeyDeletion fails", func(t *testing.T) {
		rc := newRecoverableMock()
		rc.state = types.KeyStatePendingDeletion
		rc.cancelErr = errors.New("kms: access denied")
		p := New(WithKMSClient(rc))
		defer func() { _ = p.Close() }()

		err := p.UnrevokeKey("k")
		require.Error(t, err)
		assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
		assert.Contains(t, err.Error(), "cancel key deletion")
		assert.Empty(t, rc.enabled, "EnableKey must not run when the cancellation failed")
	})
	t.Run("EnableKey fails after a successful cancel, then a retry completes the restore", func(t *testing.T) {
		rc := newRecoverableMock()
		rc.state = types.KeyStatePendingDeletion
		rc.enableErr = errors.New("kms: access denied")
		p := New(WithKMSClient(rc))
		defer func() { _ = p.Close() }()

		err := p.UnrevokeKey("k")
		require.Error(t, err)
		assert.ErrorIs(t, err, encryption.ErrEncryptionFailed)
		assert.Contains(t, err.Error(), "enable key")
		assert.Equal(t, []string{"k"}, rc.cancelled)
		assert.Equal(t, types.KeyStateDisabled, rc.state, "CancelKeyDeletion leaves the key Disabled")

		// Retry once the transient failure clears: the key is Disabled but no longer
		// pending, so only EnableKey runs — no second CancelKeyDeletion.
		rc.enableErr = nil
		require.NoError(t, p.UnrevokeKey("k"))
		assert.Equal(t, []string{"k"}, rc.cancelled, "cancellation is not repeated")
		assert.Equal(t, []string{"k"}, rc.enabled)
		assert.Equal(t, types.KeyStateEnabled, rc.state)
		state, err := p.RevocationState("k")
		require.NoError(t, err)
		assert.Equal(t, encryption.NotRevoked, state)
	})
}

// --- Context threading and the revocation timeout ---

type ctxKey struct{}

func TestProvider_DecryptErrorPath_ThreadsCallerContext(t *testing.T) {
	rc := newRevocationMock()
	rc.state = types.KeyStatePendingDeletion
	p := New(WithKMSClient(rc))
	defer func() { _ = p.Close() }()

	t.Run("values and deadline propagate to the probe", func(t *testing.T) {
		deadline := time.Now().Add(time.Hour)
		ctx, cancel := context.WithDeadline(context.WithValue(context.Background(), ctxKey{}, "trace-1"), deadline)
		defer cancel()

		_, err := p.Decrypt(ctx, "k", []byte("enc:x"))
		require.ErrorIs(t, err, encryption.ErrKeyRevoked)
		require.NotNil(t, rc.describeCtx)
		assert.Equal(t, "trace-1", rc.describeCtx.Value(ctxKey{}))
		got, ok := rc.describeCtx.Deadline()
		require.True(t, ok)
		assert.True(t, got.Equal(deadline), "the caller's own deadline is kept, not replaced")
	})
	t.Run("no caller deadline: the revocation timeout bounds the probe", func(t *testing.T) {
		rc.describeCtx = nil
		_, err := p.DecryptDataKey(context.Background(), "k", []byte("enc:x"))
		require.ErrorIs(t, err, encryption.ErrKeyRevoked)
		require.NotNil(t, rc.describeCtx)
		dl, ok := rc.describeCtx.Deadline()
		require.True(t, ok)
		assert.WithinDuration(t, time.Now().Add(DefaultRevocationTimeout), dl, 5*time.Second)
	})
	t.Run("cancelled caller context fails closed as a decryption error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rc.describeFunc = func(ctx context.Context, _ *kms.DescribeKeyInput) (*kms.DescribeKeyOutput, error) {
			return nil, ctx.Err() // what a real client does with a dead context
		}
		defer func() { rc.describeFunc = nil }()

		_, err := p.Decrypt(ctx, "k", []byte("enc:x"))
		require.Error(t, err)
		assert.ErrorIs(t, err, encryption.ErrDecryptionFailed)
		assert.NotErrorIs(t, err, encryption.ErrKeyRevoked, "an unverifiable key state must not be reported as revoked")
	})
}

func TestWithRevocationTimeout(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		assert.Equal(t, DefaultRevocationTimeout, New(WithKMSClient(&mockKMSClient{})).revocationTimeout)
	})
	t.Run("non-positive values are ignored", func(t *testing.T) {
		assert.Equal(t, DefaultRevocationTimeout, New(WithKMSClient(&mockKMSClient{}), WithRevocationTimeout(0)).revocationTimeout)
		assert.Equal(t, DefaultRevocationTimeout, New(WithKMSClient(&mockKMSClient{}), WithRevocationTimeout(-time.Second)).revocationTimeout)
	})
	t.Run("zero-value provider still bounds calls", func(t *testing.T) {
		p := &Provider{}
		assert.Equal(t, DefaultRevocationTimeout, p.timeout())
	})
	t.Run("context-free methods carry the configured deadline", func(t *testing.T) {
		rc := newRecoverableMock()
		p := New(WithKMSClient(rc), WithRevocationTimeout(90*time.Second))
		defer func() { _ = p.Close() }()

		calls := map[string]func() error{
			"RevokeKey":       func() error { return p.RevokeKey("k") },
			"IsRevoked":       func() error { _, err := p.IsRevoked("k"); return err },
			"RevocationState": func() error { _, err := p.RevocationState("k"); return err },
			"SoftRevokeKey":   func() error { return p.SoftRevokeKey("k", 7*24*time.Hour) },
			"UnrevokeKey":     func() error { return p.UnrevokeKey("k") },
		}
		for name, call := range calls {
			rc.describeCtx = nil
			require.NoError(t, call(), name)
			require.NotNil(t, rc.describeCtx, name)
			dl, ok := rc.describeCtx.Deadline()
			require.True(t, ok, "%s must bound its KMS calls", name)
			assert.WithinDuration(t, time.Now().Add(90*time.Second), dl, 5*time.Second, name)
		}
	})
	t.Run("a stalled KMS endpoint times out instead of hanging", func(t *testing.T) {
		rc := newRevocationMock()
		rc.describeFunc = func(ctx context.Context, _ *kms.DescribeKeyInput) (*kms.DescribeKeyOutput, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		p := New(WithKMSClient(rc), WithRevocationTimeout(20*time.Millisecond))
		defer func() { _ = p.Close() }()

		start := time.Now()
		err := p.RevokeKey("k")
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Less(t, time.Since(start), 5*time.Second)
		assert.Equal(t, 0, rc.scheduled)
	})
}
