package mink

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go-mink.dev/encryption"
)

// VerificationReport summarizes whether a subject's PII has been erased across its
// event footprint and the registered sibling stores.
type VerificationReport struct {
	SubjectID string
	// Verified is true iff at least one subject-tagged event was checked, no residual
	// PII remains (events AND counted sibling stores) and the footprint is complete. It
	// is never true for a report that checked nothing (see Vacuous).
	Verified       bool
	EventsChecked  int
	RedactedEvents int // encrypted events whose key is revoked (unrecoverable)

	// Vacuous is true when EventsChecked is 0: the resolver found no subject-tagged
	// event to check — the subject never had tagged events, or its subject-index entries
	// were purged (WithSubjectIndexPurge) and an index-backed resolver now resolves an
	// empty footprint. Nothing could be attested, so Verified is false and Notes says
	// so; it is not a finding of residual PII. Re-verify with a scan-backed resolver (no
	// WithResolverIndex) or after BackfillSubjectIndex.
	Vacuous bool

	// ResidualEncrypted lists "stream@version" of encrypted events whose key is still
	// live (PII recoverable).
	ResidualEncrypted []string

	// ResidualRecoverable lists "stream@version" of encrypted events whose key is only
	// SOFT-revoked — decryption is blocked but the key can still be restored via
	// UnrevokeKey within its grace window, so the PII is NOT yet permanently erased.
	// A non-empty set forces Verified=false: a certificate must never claim final
	// erasure while data is still recoverable.
	ResidualRecoverable []string

	// ResidualCleartext lists "stream@version" of subject events with no COMPLETE
	// field-encryption envelope (!HasEncryptionEnvelope): legacy cleartext, or a bare
	// "$encryption_key_id" with no encrypted-fields list / wrapped DEK — PII that
	// crypto-shredding cannot reach.
	ResidualCleartext []string

	// ResidualStores lists the registered sibling stores (WithSubjectStore) that still
	// hold rows attributable to the subject, with the count found, as reported through
	// SubjectResidualCounter. Any entry forces Verified=false.
	ResidualStores []SubjectStoreResidual

	// UncheckedStores names the registered sibling stores whose residual rows could
	// NOT be counted: the SubjectErasable does not implement SubjectResidualCounter, or
	// its underlying store lacks the counter extension (ErrResidualCountUnsupported).
	// They are neither proven clean nor proven dirty; the report says so instead of
	// certifying from the event log alone. They do not by themselves block Verified —
	// read them before relying on a verified report.
	UncheckedStores []string

	// Notes are PII-free, human-readable explanations of what limited the
	// verification (counts and store names only; never ids, fields or values).
	Notes []string

	// Partial mirrors the footprint: untagged events may exist beyond what was checked.
	Partial bool
}

// ErasureCertificate is a PII-free record that an erasure was performed and verified,
// suitable for recording via an AuditStore for Article 17 accountability.
//
// Verified is true only when the certificate could positively attest the erasure:
// every subject-tagged event checked is unrecoverable, every registered sibling store
// that could be counted is clean, the footprint is complete, every key revoked, the
// marker (if configured) written, and no read model or sibling store failed. An
// erasure that checked ZERO events but revoked keys or targeted streams (a KeyIDs-only,
// explicit-Streams or untagged-legacy erasure) is NOT verified — nothing could be
// attested — and Notes says so; a genuinely empty footprint (no streams, no keys) is
// verified with a "nothing to erase" note.
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

	// StoresVerified names the registered sibling stores (WithSubjectStore) proven,
	// through SubjectResidualCounter, to hold no row attributable to the subject at
	// certification time. A store that skipped footprint streams shared with other
	// subjects (SubjectErasureOutcome.SharedStreamsSkipped) is NOT listed — its rows on
	// those streams were neither purged nor counted — and Notes says so.
	StoresVerified []string `json:"storesVerified,omitempty"`

	// StoresUnchecked names the registered sibling stores that could not be counted
	// (no SubjectResidualCounter, or ErrResidualCountUnsupported). The certificate
	// does not attest those stores.
	StoresUnchecked []string `json:"storesUnchecked,omitempty"`

	// Notes are PII-free explanations of anything that limited the attestation
	// (counts and store names only; never ids, fields or values).
	Notes []string `json:"notes,omitempty"`
}

// CertificateSink receives an erasure certificate (no PII) for recording, e.g. via
// an AuditStore. Configure with WithCertificateSink.
type CertificateSink func(ctx context.Context, cert ErasureCertificate) error

// Verify checks that no recoverable PII remains for the subject across its event
// footprint AND the registered sibling stores (WithSubjectStore) that implement
// SubjectResidualCounter. It is deterministic — an encrypted event is erased iff its
// key is permanently revoked; a sibling store is clean iff its residual count is zero.
// Requires a configured SubjectResolver (WithEraseSubjectResolver). A sibling store
// that cannot be counted is listed under UncheckedStores rather than certified.
//
// A report must attest SOMETHING: when the resolver yields no subject-tagged event to
// check (EventsChecked == 0), the report is Vacuous — Verified is false and a note says
// so — rather than a positive attestation built on zero evidence. Sibling-store residual
// counts cover the subject id and the subject's exclusive streams only; when the
// footprint has streams shared with other subjects, a note says those stores' rows on
// the shared streams were not counted.
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
	counted, err := e.countSubjectResiduals(ctx, subjectID, fp, rep)
	if err != nil {
		return nil, NewErasureError(subjectID, err)
	}
	if n := len(rep.ResidualCleartext); n > 0 {
		rep.Notes = append(rep.Notes, cleartextNote(n))
	}
	rep.Notes = append(rep.Notes, residualNotes(rep)...)
	if n := len(fp.SharedStreams); n > 0 && (len(counted) > 0 || len(rep.ResidualStores) > 0) {
		rep.Notes = append(rep.Notes, sharedStreamsCountNote(n, len(fp.Streams)))
	}
	if rep.EventsChecked == 0 {
		rep.Vacuous = true
		rep.Verified = false
		rep.Notes = append(rep.Notes, vacuousVerifyNote)
		return rep, nil
	}
	rep.Verified = rep.clean()
	return rep, nil
}

// vacuousVerifyNote explains a report that checked no event (PII-free).
const vacuousVerifyNote = "no subject-tagged events were resolved for the subject; nothing was checked, so the report cannot attest that the subject's events are unrecoverable (a purged subject index resolves an empty footprint: re-verify with a scan-backed resolver or after BackfillSubjectIndex)"

// sharedStreamsCountNote explains that sibling-store residual counts exclude the
// footprint streams shared with other subjects (PII-free: counts only).
func sharedStreamsCountNote(shared, total int) string {
	return fmt.Sprintf("%d of %d footprint stream(s) are shared with other subjects; sibling-store residual counts cover only the subject id and the exclusive streams", shared, total)
}

// clean reports whether the report shows no residual PII (including
// still-recoverable soft-revoked keys and rows left in sibling stores) and a
// complete footprint.
func (r *VerificationReport) clean() bool {
	return len(r.ResidualEncrypted) == 0 &&
		len(r.ResidualRecoverable) == 0 &&
		len(r.ResidualCleartext) == 0 &&
		len(r.ResidualStores) == 0 &&
		!r.Partial
}

// cleartextNote describes subject events that carry no field encryption. Such events
// cannot be crypto-shredded: revoking keys leaves their PII readable, so they need
// a different remedy (ReEncryptStreamInPlace before erasure, or retention-based
// redaction / anonymization).
func cleartextNote(n int) string {
	return fmt.Sprintf("%d subject event(s) are not field-encrypted and cannot be crypto-shredded; their PII remains readable", n)
}

// residualNotes renders the PII-free notes implied by a report's residuals: counts
// and store names only.
func residualNotes(rep *VerificationReport) []string {
	var notes []string
	if n := len(rep.ResidualRecoverable); n > 0 {
		notes = append(notes, fmt.Sprintf("%d event(s) remain recoverable: their key is only soft-revoked and can still be restored within its grace window", n))
	}
	for _, rs := range rep.ResidualStores {
		notes = append(notes, fmt.Sprintf("sibling store %q still holds %d row(s) attributable to the subject", rs.Name, rs.Count))
	}
	if len(rep.UncheckedStores) > 0 {
		notes = append(notes, fmt.Sprintf("%d sibling store(s) could not be counted and are not attested: %v", len(rep.UncheckedStores), rep.UncheckedStores))
	}
	return notes
}

// countSubjectResiduals asks every registered sibling store that implements
// SubjectResidualCounter how many rows it still holds for the subject, recording
// residuals and unchecked stores on rep. It returns the names of the stores proven
// clean. A store that cannot count (no interface, or ErrResidualCountUnsupported) is
// recorded as unchecked; any other error is returned.
func (e *DataEraser) countSubjectResiduals(ctx context.Context, subjectID string, fp *SubjectFootprint, rep *VerificationReport) ([]string, error) {
	var verified []string
	for _, s := range e.subjectStores {
		name := s.ErasableName()
		counter, ok := s.(SubjectResidualCounter)
		if !ok {
			rep.UncheckedStores = append(rep.UncheckedStores, name)
			continue
		}
		n, err := counter.CountSubjectResidual(ctx, subjectID, fp)
		if err != nil {
			if errors.Is(err, ErrResidualCountUnsupported) {
				rep.UncheckedStores = append(rep.UncheckedStores, name)
				continue
			}
			return verified, fmt.Errorf("count residual rows in subject store %q: %w", name, err)
		}
		if n > 0 {
			rep.ResidualStores = append(rep.ResidualStores, SubjectStoreResidual{Name: name, Count: n})
			continue
		}
		verified = append(verified, name)
	}
	return verified, nil
}

// emitCertificate verifies the erased subject and sends a PII-free certificate to
// the configured sink. Used by Erase when WithCertificateSink is set. It returns the
// certificate (so Erase can gate follow-up steps such as the subject-index purge on
// Verified) and the sink error (if any) so strict accountability can surface it
// fatally.
func (e *DataEraser) emitCertificate(ctx context.Context, subjectID string, result *ErasureResult) (ErasureCertificate, error) {
	cert := e.buildCertificate(ctx, subjectID, result)
	if err := e.certSink(ctx, cert); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("certificate sink: %w", err))
		return cert, err
	}
	return cert, nil
}

// buildCertificate runs the erasure verification over result and returns the PII-free
// certificate WITHOUT sending it anywhere. It is the attestation behind both the
// emitted certificate (emitCertificate) and the internal check that gates the
// subject-index purge when no sink is configured. Verification failures are recorded
// on result.Errors.
func (e *DataEraser) buildCertificate(ctx context.Context, subjectID string, result *ErasureResult) ErasureCertificate {
	cert := ErasureCertificate{
		SubjectID:   subjectID,
		VerifiedAt:  time.Now(),
		KeysRevoked: len(result.KeysRevoked),
		Partial:     result.Partial,
		SideEffects: result.SideEffects,
	}
	verified := true

	vr := &VerificationReport{SubjectID: subjectID, Partial: result.Partial}
	if err := e.verifyStreams(ctx, subjectID, result.Streams, vr); err != nil {
		verified = false
		result.Errors = append(result.Errors, fmt.Errorf("certificate event verification: %w", err))
		cert.Notes = append(cert.Notes, "event verification could not be completed; the events are not attested")
	}
	storesVerified, err := e.countSubjectResiduals(ctx, subjectID, result.footprint(subjectID), vr)
	if err != nil {
		verified = false
		result.Errors = append(result.Errors, fmt.Errorf("certificate residual count: %w", err))
		cert.Notes = append(cert.Notes, "sibling-store residual count failed; the sibling stores are not attested")
	}
	// A store that left shared streams alone was counted over the exclusive ids only:
	// its rows for the subject on the shared streams are neither purged nor counted, so
	// it cannot be listed as verified and the certificate cannot attest the erasure.
	skippedShared := map[string]int{}
	for _, ss := range result.SubjectStores {
		if ss.SharedStreamsSkipped > 0 {
			skippedShared[ss.Name] = ss.SharedStreamsSkipped
		}
	}
	if len(skippedShared) > 0 {
		kept := storesVerified[:0:0]
		for _, name := range storesVerified {
			if _, skipped := skippedShared[name]; !skipped {
				kept = append(kept, name)
			}
		}
		storesVerified = kept
	}
	cert.EventsChecked = vr.EventsChecked
	cert.StoresVerified = storesVerified
	cert.StoresUnchecked = vr.UncheckedStores
	if !vr.clean() {
		verified = false
	}
	cert.Notes = append(cert.Notes, residualNotes(vr)...)

	// A certificate must attest SOMETHING. When keys were revoked (or failed to
	// revoke) or streams were targeted but not one subject-tagged event could be
	// checked — a KeyIDs-only, explicit-Streams or untagged-legacy erasure — the
	// certificate is vacuous and must not claim verification. A genuinely empty
	// footprint (nothing resolved, nothing revoked) is honestly "nothing to erase".
	attempted := len(result.KeysRevoked) > 0 || len(result.KeysFailed) > 0 || len(result.Streams) > 0
	switch {
	case vr.EventsChecked == 0 && attempted:
		verified = false
		cert.Notes = append(cert.Notes, "no subject-tagged events could be verified: the erasure was scoped by key ids, explicit streams or untagged events, so the certificate cannot attest that the subject's events are unrecoverable")
	case vr.EventsChecked == 0:
		cert.Notes = append(cert.Notes, "nothing to erase: the subject has no resolved streams or keys")
	}
	// A key that failed to revoke leaves everything under it recoverable, whatever
	// the event check saw (the events may be untagged or outside the checked streams).
	if n := len(result.KeysFailed); n > 0 {
		verified = false
		cert.Notes = append(cert.Notes, fmt.Sprintf("%d key(s) failed to revoke; data under them remains recoverable", n))
	}
	// Cleartext subject events survive crypto-shredding untouched.
	if result.CleartextEvents > 0 {
		cert.Notes = append(cert.Notes, cleartextNote(result.CleartextEvents))
	}
	cert.Notes = append(cert.Notes, result.Notes...)

	// When a marker stream is configured, the certificate must not claim verified
	// erasure unless the append-only marker was actually written — otherwise a lost
	// marker leaves a "verified" receipt with no durable erasure record.
	if e.markerStream != "" && !result.MarkerWritten {
		verified = false
		cert.Notes = append(cert.Notes, "the erasure marker was not written; no durable erasure record exists")
	}
	// A registered sibling store that failed, or that could not be purged (Skipped,
	// e.g. it lacks the subject-purger interface), may still hold the subject's PII —
	// so the erasure is not fully verified. Verify() itself covers events and counted
	// sibling stores; per-store erase outcomes are known here via result.SubjectStores.
	for _, ss := range result.SubjectStores {
		if ss.Err != "" || ss.Skipped {
			verified = false
			cert.Notes = append(cert.Notes, fmt.Sprintf("sibling store %q was not erased (failed or skipped)", ss.Name))
			continue
		}
		if ss.SharedStreamsSkipped > 0 {
			// Not "skipped" (the store IS supported): it purged the exclusive streams and
			// deliberately left the shared ones, where the subject's rows may remain.
			verified = false
			cert.Notes = append(cert.Notes, fmt.Sprintf("sibling store %q left %d footprint stream(s) shared with other subjects untouched; rows for the subject on those streams may remain and are not attested", ss.Name, ss.SharedStreamsSkipped))
		}
	}
	// A read model that could not be redacted (ResidualReadModels) may still serve the
	// subject's plaintext PII from the read side, so it must block verification too —
	// key revocation alone does not make the erasure complete.
	if n := len(result.ResidualReadModels); n > 0 {
		verified = false
		cert.Notes = append(cert.Notes, fmt.Sprintf("%d read model(s) could not be redacted", n))
	}
	cert.Verified = verified
	return cert
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
			// Only the target subject's events matter. A resolved stream can be shared
			// with other subjects; checking their events would inflate EventsChecked and
			// produce false residuals. Untagged (legacy) events are covered by Partial.
			if !eventTagsSubject(se.Metadata, subjectID) {
				continue
			}
			rep.EventsChecked++
			ref := fmt.Sprintf("%s@%d", se.StreamID, se.Version)
			// Same predicate as key discovery: only a complete envelope is ciphertext a
			// revocation erases. Anything less (legacy plaintext, a bare key id, a damaged
			// envelope) is readable or unshreddable PII — residual cleartext either way.
			if !HasEncryptionEnvelope(se.Metadata) {
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
