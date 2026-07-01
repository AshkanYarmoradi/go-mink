package mink

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go-mink.dev/encryption"
)

// VerificationReport summarizes whether a subject's PII has been erased across its
// event footprint.
type VerificationReport struct {
	SubjectID      string
	Verified       bool // true iff no residual PII and the footprint is complete
	EventsChecked  int
	RedactedEvents int // encrypted events whose key is revoked (unrecoverable)

	// ResidualEncrypted lists "stream@version" of encrypted events whose key is still
	// live (PII recoverable).
	ResidualEncrypted []string

	// ResidualRecoverable lists "stream@version" of encrypted events whose key is only
	// SOFT-revoked — decryption is blocked but the key can still be restored via
	// UnrevokeKey within its grace window, so the PII is NOT yet permanently erased.
	// A non-empty set forces Verified=false: a certificate must never claim final
	// erasure while data is still recoverable.
	ResidualRecoverable []string

	// ResidualCleartext lists "stream@version" of events with no encryption (possible
	// legacy cleartext PII that crypto-shredding cannot reach).
	ResidualCleartext []string

	// Partial mirrors the footprint: untagged events may exist beyond what was checked.
	Partial bool
}

// ErasureCertificate is a PII-free record that an erasure was performed and verified,
// suitable for recording via an AuditStore for Article 17 accountability.
type ErasureCertificate struct {
	SubjectID     string    `json:"subjectId"`
	VerifiedAt    time.Time `json:"verifiedAt"`
	Verified      bool      `json:"verified"`
	EventsChecked int       `json:"eventsChecked"`
	KeysRevoked   int       `json:"keysRevoked"`
	Partial       bool      `json:"partial"`

	// SideEffects names the external-PII domains erased alongside the event store
	// (no PII), e.g. "blob-storage", "search-index".
	SideEffects []string `json:"sideEffects,omitempty"`
}

// CertificateSink receives an erasure certificate (no PII) for recording, e.g. via
// an AuditStore. Configure with WithCertificateSink.
type CertificateSink func(ctx context.Context, cert ErasureCertificate) error

// Verify checks that no recoverable PII remains for the subject across its event
// footprint. It is deterministic — an encrypted event is erased iff its key is
// revoked. Requires a configured SubjectResolver (WithEraseSubjectResolver).
func (e *DataEraser) Verify(ctx context.Context, subjectID string) (*VerificationReport, error) {
	if subjectID == "" {
		return nil, ErrErasureSubjectRequired
	}
	if e.resolver == nil {
		return nil, NewErasureError(subjectID, fmt.Errorf("Verify requires a subject resolver (WithEraseSubjectResolver)"))
	}
	fp, err := e.resolver.Resolve(ctx, subjectID)
	if err != nil {
		return nil, NewErasureError(subjectID, err)
	}
	rep := &VerificationReport{SubjectID: subjectID, Partial: fp.Partial}
	if err := e.verifyStreams(ctx, subjectID, fp.Streams, rep); err != nil {
		return nil, err
	}
	rep.Verified = rep.clean()
	return rep, nil
}

// clean reports whether the report shows no residual PII (including
// still-recoverable soft-revoked keys) and a complete footprint.
func (r *VerificationReport) clean() bool {
	return len(r.ResidualEncrypted) == 0 &&
		len(r.ResidualRecoverable) == 0 &&
		len(r.ResidualCleartext) == 0 &&
		!r.Partial
}

// emitCertificate verifies the erased subject and sends a PII-free certificate to
// the configured sink. Used by Erase when WithCertificateSink is set. Returns the
// sink error (if any) so strict accountability can surface it fatally.
func (e *DataEraser) emitCertificate(ctx context.Context, subjectID string, result *ErasureResult) error {
	cert := ErasureCertificate{
		SubjectID:   subjectID,
		VerifiedAt:  time.Now(),
		KeysRevoked: len(result.KeysRevoked),
		Partial:     result.Partial,
		SideEffects: result.SideEffects,
	}
	vr := &VerificationReport{SubjectID: subjectID, Partial: result.Partial}
	if err := e.verifyStreams(ctx, subjectID, result.Streams, vr); err == nil {
		cert.EventsChecked = vr.EventsChecked
		cert.Verified = vr.clean()
	}
	// When a marker stream is configured, the certificate must not claim verified
	// erasure unless the append-only marker was actually written — otherwise a lost
	// marker leaves a "verified" receipt with no durable erasure record.
	if e.markerStream != "" && !result.MarkerWritten {
		cert.Verified = false
	}
	if err := e.certSink(ctx, cert); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("certificate sink: %w", err))
		return err
	}
	return nil
}

func (e *DataEraser) verifyStreams(ctx context.Context, subjectID string, streams []string, rep *VerificationReport) error {
	cfg := e.store.EncryptionConfig()
	for _, streamID := range streams {
		stored, err := e.store.LoadRaw(ctx, streamID, 0)
		if err != nil {
			if errors.Is(err, ErrStreamNotFound) {
				continue
			}
			return NewErasureError(subjectID, fmt.Errorf("load stream %q: %w", streamID, err))
		}
		for _, se := range stored {
			rep.EventsChecked++
			ref := fmt.Sprintf("%s@%d", se.StreamID, se.Version)
			if !IsEncrypted(se.Metadata) {
				rep.ResidualCleartext = append(rep.ResidualCleartext, ref)
				continue
			}
			// Only a PERMANENT revocation counts as erased. A soft-revoked key
			// (restorable within its grace window) leaves the PII recoverable, so it
			// is surfaced separately and must block verification.
			state := encryption.NotRevoked
			if cfg != nil {
				if s, err := cfg.RevocationState(GetEncryptionKeyID(se.Metadata)); err == nil {
					state = s
				}
			}
			switch state {
			case encryption.Revoked:
				rep.RedactedEvents++
			case encryption.SoftRevoked:
				rep.ResidualRecoverable = append(rep.ResidualRecoverable, ref)
			default:
				rep.ResidualEncrypted = append(rep.ResidualEncrypted, ref)
			}
		}
	}
	return nil
}
