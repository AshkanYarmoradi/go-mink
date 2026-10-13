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
	// Verified is true iff at least one event attributed to the subject was checked, no
	// residual PII remains (events AND counted sibling stores) and the footprint is
	// complete. It is never true for a report that checked nothing (see Vacuous).
	Verified bool
	// EventsChecked counts the events attributed to the subject — by persisted tag, or
	// derived by the tagger (see WithResolverTagger) — that were checked.
	EventsChecked  int
	RedactedEvents int // encrypted events whose key is revoked (unrecoverable)

	// Vacuous is true when EventsChecked is 0: the resolver found no event attributed to
	// the subject to check — the subject never had attributable events, its
	// subject-index entries were purged (WithSubjectIndexPurge) and an index-backed
	// resolver now resolves an empty footprint, or its events were derived-only (untagged,
	// attributed through the tagger) and encrypted, so revoking their key also made them
	// unattributable (see UnattributedRedacted; the certificate Erase emits verifies the
	// events it matched before revoking, and is the attestation for that case). Nothing
	// could be attested, so Verified is false and Notes says so; it is not a finding of
	// residual PII. Re-verify with a scan-backed resolver (no WithResolverIndex) or after
	// BackfillSubjectIndex.
	Vacuous bool

	// UnattributedRecoverable lists "stream@version" of events in the subject's footprint
	// streams that carry no subject tag and that the tagger could not attribute to ANY
	// subject (no tagger, nothing derived, or encrypted and undecryptable), and whose
	// payload is still readable or recoverable: no complete encryption envelope, or a key
	// that is live or only soft-revoked. Any of them may be the subject's own PII, so they
	// were not checked as the subject's AND force Partial — the footprint cannot be proven
	// complete, whatever the resolver said.
	UnattributedRecoverable []string

	// UnattributedRedacted lists "stream@version" of events in the subject's footprint
	// streams that could not be attributed to any subject but are permanently
	// crypto-shredded (complete envelope, key revoked): whoever they belonged to, no PII
	// is recoverable from their encrypted fields, so they neither block nor count toward
	// the attestation. A derived-only encrypted event lands here after its key is revoked.
	UnattributedRedacted []string

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

	// Partial is true when completeness cannot be proven: the footprint was Partial, or
	// the subject's streams hold unattributable events whose PII is still recoverable
	// (UnattributedRecoverable).
	Partial bool
}

// ErasureCertificate is a PII-free record that an erasure was performed and verified,
// suitable for recording via an AuditStore for Article 17 accountability.
//
// Verified is true only when the certificate could positively attest the erasure:
// every event attributed to the subject that was checked is unrecoverable, every
// registered sibling store that could be counted is clean, the footprint is complete,
// every key revoked, the marker (if configured) written, and no read model or sibling
// store failed. The events checked are the ones the erasure attributed to the subject
// (persisted tag or tagger-derived) BEFORE revoking — so a derived-only encrypted event,
// unattributable once shredded, is still attested — plus any attributable event found in
// the subject's streams afterwards. An erasure that checked ZERO such events but revoked
// keys or targeted streams (a KeyIDs-only, explicit-Streams or untagged-legacy erasure)
// is NOT verified — nothing could be attested — and Notes says so; a genuinely empty
// footprint (no streams, no keys) is verified with a "nothing to erase" note.
type ErasureCertificate struct {
	SubjectID     string    `json:"subjectId"`
	VerifiedAt    time.Time `json:"verifiedAt"`
	Verified      bool      `json:"verified"`
	EventsChecked int       `json:"eventsChecked"`
	KeysRevoked   int       `json:"keysRevoked"`
	// Partial is true when the erasure's footprint was Partial, or when the verification
	// found unattributable events with still-recoverable PII in the subject's streams
	// (VerificationReport.UnattributedRecoverable).
	Partial bool `json:"partial"`

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
// Events are attributed under the shared rule (persisted tag, or the tagger — the
// resolver's or the store's — run over an untagged event's plaintext). An event in the
// subject's streams that nobody can attribute is not silently skipped: it is listed as
// UnattributedRecoverable (and forces Partial) while its PII is still recoverable, or as
// UnattributedRedacted once permanently crypto-shredded.
//
// A report must attest SOMETHING: when the resolver yields no event attributed to the
// subject to check (EventsChecked == 0), the report is Vacuous — Verified is false and a
// note says so — rather than a positive attestation built on zero evidence. Sibling-store
// residual counts cover the subject id and the subject's exclusive streams only; when the
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
	if err := e.verifyStreams(ctx, subjectID, fp.Streams, rep, nil); err != nil {
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
const vacuousVerifyNote = "no events attributed to the subject were resolved; nothing was checked, so the report cannot attest that the subject's events are unrecoverable (a purged subject index resolves an empty footprint: re-verify with a scan-backed resolver or after BackfillSubjectIndex; an untagged event whose subject the tagger derived can no longer be attributed once its key is revoked: the certificate emitted by Erase, which verifies the events matched before revoking, is the attestation for it)"

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
	if len(rep.UnattributedRecoverable)+len(rep.UnattributedRedacted) > 0 {
		notes = append(notes, unattributedNote(len(rep.UnattributedRecoverable), len(rep.UnattributedRedacted)))
	}
	for _, rs := range rep.ResidualStores {
		notes = append(notes, fmt.Sprintf("sibling store %q still holds %d row(s) attributable to the subject", rs.Name, rs.Count))
	}
	if len(rep.UncheckedStores) > 0 {
		notes = append(notes, fmt.Sprintf("%d sibling store(s) could not be counted and are not attested: %v", len(rep.UncheckedStores), rep.UncheckedStores))
	}
	return notes
}

// unattributedNote explains the events in the subject's streams that nobody could
// attribute (PII-free: counts only).
func unattributedNote(recoverable, redacted int) string {
	return fmt.Sprintf("%d event(s) in the subject's streams could not be attributed to any subject and were not checked as the subject's: %d still readable or recoverable (possibly the subject's own, so completeness cannot be proven), %d permanently crypto-shredded (no recoverable PII)",
		recoverable+redacted, recoverable, redacted)
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
	d := &certificateDraft{verified: true, cert: ErasureCertificate{
		SubjectID:   subjectID,
		VerifiedAt:  time.Now(),
		KeysRevoked: len(result.KeysRevoked),
		Partial:     result.Partial,
		SideEffects: result.SideEffects,
	}}

	vr := e.attestEvidence(ctx, subjectID, result, d)
	d.gateVacuous(vr.EventsChecked, result)
	d.gateKeyOutcome(result)

	// When a marker stream is configured, the certificate must not claim verified
	// erasure unless the append-only marker was actually written — otherwise a lost
	// marker leaves a "verified" receipt with no durable erasure record.
	if e.markerStream != "" && !result.MarkerWritten {
		d.block("the erasure marker was not written; no durable erasure record exists")
	}
	d.gateSubjectStores(result.SubjectStores)
	// A read model that could not be redacted (ResidualReadModels) may still serve the
	// subject's plaintext PII from the read side, so it must block verification too —
	// key revocation alone does not make the erasure complete.
	if n := len(result.ResidualReadModels); n > 0 {
		d.block(fmt.Sprintf("%d read model(s) could not be redacted", n))
	}
	d.cert.Verified = d.verified
	return d.cert
}

// certificateDraft is the ErasureCertificate that buildCertificate assembles, together
// with its running verdict. Every finding that denies attestation goes through block,
// which clears the verdict AND explains why in a PII-free note, so the two cannot drift
// apart; note records an explanation that leaves the verdict alone.
type certificateDraft struct {
	cert     ErasureCertificate
	verified bool
}

// block denies attestation and records the (PII-free) reason.
func (d *certificateDraft) block(note string) {
	d.verified = false
	d.cert.Notes = append(d.cert.Notes, note)
}

// note records a (PII-free) explanation without affecting the verdict.
func (d *certificateDraft) note(note string) {
	d.cert.Notes = append(d.cert.Notes, note)
}

// attestEvidence runs the verification behind a certificate — the subject's events
// (verifyStreams) and the registered sibling stores' residual counts
// (countSubjectResiduals) — and records the evidence on the draft: EventsChecked, the
// stores it may list as verified (attestableStores), the stores that could not be
// counted, and the residual notes. A verification step that fails (also recorded on
// result.Errors) or any residual PII denies attestation. The report is returned so the
// caller can judge whether the certificate attests anything at all.
func (e *DataEraser) attestEvidence(ctx context.Context, subjectID string, result *ErasureResult, d *certificateDraft) *VerificationReport {
	vr := &VerificationReport{SubjectID: subjectID, Partial: result.Partial}
	// The events the erasure attributed to the subject before revoking (result.attributed)
	// are verified as the subject's even if the revoke made them unattributable.
	if err := e.verifyStreams(ctx, subjectID, result.Streams, vr, result.attributed); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("certificate event verification: %w", err))
		d.block("event verification could not be completed; the events are not attested")
	}
	// Unattributable events with recoverable PII in the subject's streams make the
	// verification Partial; the certificate must say so too.
	d.cert.Partial = d.cert.Partial || vr.Partial
	storesVerified, err := e.countSubjectResiduals(ctx, subjectID, result.footprint(subjectID), vr)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("certificate residual count: %w", err))
		d.block("sibling-store residual count failed; the sibling stores are not attested")
	}
	d.cert.EventsChecked = vr.EventsChecked
	d.cert.StoresVerified = attestableStores(storesVerified, result.SubjectStores)
	d.cert.StoresUnchecked = vr.UncheckedStores
	if !vr.clean() {
		d.verified = false
	}
	d.cert.Notes = append(d.cert.Notes, residualNotes(vr)...)
	return vr
}

// attestableStores returns the sibling stores, among those proven clean by their
// residual count, that a certificate may list as verified. A store that left footprint
// streams shared with other subjects alone (SubjectErasureOutcome.SharedStreamsSkipped)
// was counted over the exclusive ids only: its rows for the subject on the shared
// streams are neither purged nor counted, so it cannot be listed as verified (and
// gateSubjectStores denies the attestation).
func attestableStores(storesVerified []string, outcomes []SubjectErasureOutcome) []string {
	skippedShared := map[string]struct{}{}
	for _, ss := range outcomes {
		if ss.SharedStreamsSkipped > 0 {
			skippedShared[ss.Name] = struct{}{}
		}
	}
	if len(skippedShared) == 0 {
		return storesVerified
	}
	kept := storesVerified[:0:0]
	for _, name := range storesVerified {
		if _, skipped := skippedShared[name]; !skipped {
			kept = append(kept, name)
		}
	}
	return kept
}

// gateVacuous enforces that a certificate attests SOMETHING. When keys were revoked (or
// failed to revoke) or streams were targeted but not one event attributed to the subject
// could be checked — a KeyIDs-only, explicit-Streams or untagged-legacy erasure — the
// certificate is vacuous and must not claim verification. A genuinely empty footprint
// (nothing resolved, nothing revoked) is honestly "nothing to erase".
func (d *certificateDraft) gateVacuous(eventsChecked int, result *ErasureResult) {
	if eventsChecked > 0 {
		return
	}
	attempted := len(result.KeysRevoked) > 0 || len(result.KeysFailed) > 0 || len(result.Streams) > 0
	if attempted {
		d.block("no events attributed to the subject could be verified: the erasure was scoped by key ids, explicit streams or unattributable events, so the certificate cannot attest that the subject's events are unrecoverable")
		return
	}
	d.note("nothing to erase: the subject has no resolved streams or keys")
}

// gateKeyOutcome records what the key revocation left behind. A key that failed to
// revoke leaves everything under it recoverable, whatever the event check saw (the
// events may be untagged or outside the checked streams), so it denies attestation.
// Cleartext subject events survive crypto-shredding untouched and are noted, and the
// erasure's own notes are carried onto the certificate.
func (d *certificateDraft) gateKeyOutcome(result *ErasureResult) {
	if n := len(result.KeysFailed); n > 0 {
		d.block(fmt.Sprintf("%d key(s) failed to revoke; data under them remains recoverable", n))
	}
	if result.CleartextEvents > 0 {
		d.note(cleartextNote(result.CleartextEvents))
	}
	d.cert.Notes = append(d.cert.Notes, result.Notes...)
}

// gateSubjectStores denies attestation for every registered sibling store that failed,
// or that could not be purged (Skipped, e.g. it lacks the subject-purger interface) —
// either may still hold the subject's PII — and for every store that purged the
// exclusive streams but deliberately left the ones shared with other subjects, where the
// subject's rows may remain. Verify() itself covers events and counted sibling stores;
// the per-store erase outcomes are known only here, via result.SubjectStores.
func (d *certificateDraft) gateSubjectStores(outcomes []SubjectErasureOutcome) {
	for _, ss := range outcomes {
		if ss.Err != "" || ss.Skipped {
			d.block(fmt.Sprintf("sibling store %q was not erased (failed or skipped)", ss.Name))
			continue
		}
		if ss.SharedStreamsSkipped > 0 {
			// Not "skipped" (the store IS supported): it purged the exclusive streams and
			// deliberately left the shared ones, where the subject's rows may remain.
			d.block(fmt.Sprintf("sibling store %q left %d footprint stream(s) shared with other subjects untouched; rows for the subject on those streams may remain and are not attested", ss.Name, ss.SharedStreamsSkipped))
		}
	}
}

// verifyStreams checks every event of the given streams that is the subject's: one the
// erasure attributed to the subject before revoking (attributed, nil for a standalone
// Verify) or one attributeSubjects attributes to the subject now (persisted tag, or
// derived by the eraser's tagger). A resolved stream can be shared with other subjects;
// their events are skipped, since checking them would inflate EventsChecked and produce
// false residuals. An event nobody can attribute is NOT silently skipped: it is recorded
// as UnattributedRecoverable (forcing Partial) while its PII is still readable or
// recoverable, or as UnattributedRedacted once permanently crypto-shredded.
func (e *DataEraser) verifyStreams(ctx context.Context, subjectID string, streams []string, rep *VerificationReport, attributed erasureMatches) error {
	v := &streamVerifier{eraser: e, subjectID: subjectID, attributed: attributed, rep: rep, cfg: e.store.EncryptionConfig()}
	for _, streamID := range streams {
		stored, err := e.store.LoadRaw(ctx, streamID, 0)
		if err != nil {
			if errors.Is(err, ErrStreamNotFound) {
				continue
			}
			return NewErasureError(subjectID, fmt.Errorf("load stream %q: %w", streamID, err))
		}
		v.verifyStream(ctx, stored)
	}
	return nil
}

// streamVerifier carries what verifyStreams threads through every event of the
// subject's streams: whose events are checked, which ones the erasure attributed before
// revoking, the report being filled, and the encryption config that answers a key's
// revocation state.
type streamVerifier struct {
	eraser     *DataEraser
	subjectID  string
	attributed erasureMatches
	rep        *VerificationReport
	cfg        *FieldEncryptionConfig
}

// verifyStream checks one loaded stream: each event that is the subject's
// (ownsEvent) is counted and classified on the report (recordChecked); the rest are
// skipped — another subject's, or filed as unattributed on the way out.
func (v *streamVerifier) verifyStream(ctx context.Context, stored []StoredEvent) {
	for _, se := range stored {
		ref := fmt.Sprintf("%s@%d", se.StreamID, se.Version)
		if !v.ownsEvent(ctx, se, ref) {
			continue // another subject's event, or nobody's to check
		}
		v.rep.recordChecked(ref, se.Metadata, v.cfg)
	}
}

// ownsEvent reports whether se is the subject's to check: the erasure attributed it to
// the subject before revoking (attributed), or attributeSubjects attributes it to the
// subject now. An event nobody can attribute is NOT silently skipped: it is filed on
// the report (recordUnattributed) before ownsEvent reports false.
func (v *streamVerifier) ownsEvent(ctx context.Context, se StoredEvent, ref string) bool {
	if v.attributed.has(se) {
		return true
	}
	at := v.eraser.attribute(ctx, se)
	if at.to(v.subjectID) {
		return true
	}
	if !at.attributed() {
		v.rep.recordUnattributed(ref, se.Metadata, v.cfg)
	}
	return false
}

// recordChecked counts one of the subject's events as checked and classifies it. Same
// predicate as key discovery: only a complete envelope is ciphertext a revocation
// erases. Anything less (legacy plaintext, a bare key id, a damaged envelope) is
// readable or unshreddable PII — residual cleartext either way. Of the encrypted events
// only a PERMANENT revocation counts as erased: a soft-revoked key (restorable within
// its grace window) leaves the PII recoverable, so it is surfaced separately and must
// block verification.
func (r *VerificationReport) recordChecked(ref string, m Metadata, cfg *FieldEncryptionConfig) {
	r.EventsChecked++
	if !HasEncryptionEnvelope(m) {
		r.ResidualCleartext = append(r.ResidualCleartext, ref)
		return
	}
	switch revocationState(cfg, m) {
	case encryption.Revoked:
		r.RedactedEvents++
	case encryption.SoftRevoked:
		r.ResidualRecoverable = append(r.ResidualRecoverable, ref)
	default:
		r.ResidualEncrypted = append(r.ResidualEncrypted, ref)
	}
}

// recordUnattributed files an event of the subject's streams that nobody could
// attribute: permanently crypto-shredded (complete envelope, key revoked) events hold no
// recoverable PII and are only listed; anything else may be the subject's own readable
// or recoverable PII, so it is listed AND forces Partial.
func (r *VerificationReport) recordUnattributed(ref string, m Metadata, cfg *FieldEncryptionConfig) {
	if HasEncryptionEnvelope(m) && revocationState(cfg, m) == encryption.Revoked {
		r.UnattributedRedacted = append(r.UnattributedRedacted, ref)
		return
	}
	r.UnattributedRecoverable = append(r.UnattributedRecoverable, ref)
	r.Partial = true
}

// revocationState returns the revocation state of the key an event's envelope names, or
// NotRevoked when there is no encryption config or the provider cannot say.
func revocationState(cfg *FieldEncryptionConfig, m Metadata) encryption.RevocationState {
	if cfg == nil {
		return encryption.NotRevoked
	}
	state, err := cfg.RevocationState(GetEncryptionKeyID(m))
	if err != nil {
		return encryption.NotRevoked
	}
	return state
}
