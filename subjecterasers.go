package mink

import (
	"context"
	"strings"

	"go-mink.dev/adapters"
)

// Footprint-aware purger and counter extensions, aliased from the adapters package
// (exactly like SubjectAuditPurger, SubjectSagaPurger, SubjectOutboxPurger and
// SubjectIdempotencyPurger) so applications and custom stores can name them without
// importing adapters. The built-in erasers below prefer these over the legacy
// id-equality purgers: a legacy purger matches one column against the BARE subject
// id, which never matches the rows the library itself writes (see SubjectFootprintIDs).
// Both built-in adapters (memory, postgres) implement them.
type (
	// SubjectOutboxFootprintPurger deletes outbox messages by producing stream id.
	SubjectOutboxFootprintPurger = adapters.SubjectOutboxFootprintPurger
	// SubjectAuditFootprintPurger deletes audit entries by targeted aggregate/stream id.
	SubjectAuditFootprintPurger = adapters.SubjectAuditFootprintPurger
	// SubjectIdempotencyFootprintPurger deletes idempotency records by aggregate/stream id.
	SubjectIdempotencyFootprintPurger = adapters.SubjectIdempotencyFootprintPurger
	// SubjectSagaFootprintPurger deletes saga states by correlation id.
	SubjectSagaFootprintPurger = adapters.SubjectSagaFootprintPurger
	// SubjectOutboxCounter counts outbox messages by producing stream id.
	SubjectOutboxCounter = adapters.SubjectOutboxCounter
	// SubjectAuditCounter counts audit entries by actor or targeted aggregate/stream id.
	SubjectAuditCounter = adapters.SubjectAuditCounter
	// SubjectIdempotencyCounter counts idempotency records by aggregate/stream id.
	SubjectIdempotencyCounter = adapters.SubjectIdempotencyCounter
	// SubjectSagaCounter counts saga states by correlation id.
	SubjectSagaCounter = adapters.SubjectSagaCounter
)

// Compile-time checks: every built-in eraser can also prove itself clean, and every
// store-backed one can be switched to derived aggregate ids by DataEraser.
var (
	_ SubjectResidualCounter = (*auditSubjectEraser)(nil)
	_ SubjectResidualCounter = (*sagaSubjectEraser)(nil)
	_ SubjectResidualCounter = (*outboxSubjectEraser)(nil)
	_ SubjectResidualCounter = (*idempotencySubjectEraser)(nil)
	_ SubjectResidualCounter = (*snapshotSubjectEraser)(nil)

	_ derivedAggregateIDsOptIn = (*auditSubjectEraser)(nil)
	_ derivedAggregateIDsOptIn = (*sagaSubjectEraser)(nil)
	_ derivedAggregateIDsOptIn = (*outboxSubjectEraser)(nil)
	_ derivedAggregateIDsOptIn = (*idempotencySubjectEraser)(nil)
)

// derivedAggregateIDsOptIn is implemented by the built-in store-backed erasers so that
// NewDataEraser can hand them the WithDerivedAggregateIDs setting without widening the
// SubjectErasable contract. It returns a configured COPY (the registered eraser may be
// shared with other DataErasers).
type derivedAggregateIDsOptIn interface {
	withDerivedAggregateIDs() SubjectErasable
}

// SubjectFootprintIDs returns the de-duplicated, order-preserving set of identifiers
// under which rows attributable to subjectID — and to NO other subject — are keyed in
// sibling stores:
//
//   - the bare subject id itself (rows an application keys by subject, and audit rows
//     whose Actor is the subject);
//   - every EXCLUSIVE stream id in the resolved footprint (fp.ExclusiveStreams: Streams
//     minus SharedStreams) — the outbox's AggregateID is the producing STREAM id (e.g.
//     "User-u1"), and audit / idempotency rows may carry it.
//
// Streams shared with other subjects (SubjectFootprint.SharedStreams) are deliberately
// left out: rows keyed by such a stream — a co-tenant's pending outbox messages, an
// in-flight saga, another subject's audit or idempotency rows — cannot be attributed
// to this subject alone, and the built-in erasers report them as
// SubjectErasureOutcome.SharedStreamsSkipped instead of deleting them.
//
// Aggregate ids derived from stream ids ("User-u1" → "u1") are NOT included: audit and
// idempotency rows and saga correlation ids carry the aggregate id WITHOUT its type, so
// a derived id collides across aggregate types ("Order-123" and "User-123" both derive
// "123") and would purge other aggregates' — other subjects' — rows. Opt in with
// SubjectFootprintIDsWithDerived / WithDerivedAggregateIDs when aggregate ids are
// globally unique.
//
// Empty strings are dropped and a nil footprint yields just the subject id. The
// built-in erasers pass this set to the footprint-aware purgers and counters; a custom
// SubjectErasable can use it to target exactly the same rows.
func SubjectFootprintIDs(subjectID string, fp *SubjectFootprint) []string {
	return footprintIDs(subjectID, fp, false)
}

// SubjectFootprintIDsWithDerived is SubjectFootprintIDs plus, for each EXCLUSIVE
// footprint stream, the aggregate id derived from it: stream ids are built as
// AggregateType + "-" + AggregateID, so the part after the FIRST '-' is the raw
// aggregate id ("User-u1" → "u1", "Order-ord-42" → "ord-42"; aggregate ids may
// themselves contain '-'), which is what the audit and idempotency AggregateID columns
// and most saga correlation ids hold.
//
// The derived id is matched WITHOUT the aggregate type, so this is only exact when raw
// aggregate ids are unique across aggregate types (UUIDs, or ids that embed their
// type). With per-type sequential ids a footprint stream "Order-123" also purges the
// audit / idempotency rows and sagas of "Invoice-123" — possibly another subject's.
// Derived ids are never taken from shared streams. DataEraser switches its built-in
// erasers to this set with WithDerivedAggregateIDs.
func SubjectFootprintIDsWithDerived(subjectID string, fp *SubjectFootprint) []string {
	return footprintIDs(subjectID, fp, true)
}

func footprintIDs(subjectID string, fp *SubjectFootprint, derived bool) []string {
	streams := fp.ExclusiveStreams()
	ids := make([]string, 0, 1+2*len(streams))
	ids = append(ids, subjectID)
	for _, streamID := range streams {
		ids = append(ids, streamID)
		if !derived {
			continue
		}
		if agg, ok := derivedAggregateID(streamID); ok {
			ids = append(ids, agg)
		}
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// derivedAggregateID extracts the aggregate id from a "Type-id" stream id: the part
// after the FIRST '-' (aggregate ids may themselves contain '-'). ok is false when
// the stream id has no '-' or an empty type or id part.
func derivedAggregateID(streamID string) (string, bool) {
	i := strings.IndexByte(streamID, '-')
	if i <= 0 || i == len(streamID)-1 {
		return "", false
	}
	return streamID[i+1:], true
}

// sharedStreamCount is the number of footprint streams a stream-keyed purge leaves
// untouched because they are shared with other subjects.
func sharedStreamCount(fp *SubjectFootprint) int {
	if fp == nil {
		return 0
	}
	return len(fp.Streams) - len(fp.ExclusiveStreams())
}

// footprintIDSet is the id set a built-in eraser purges and counts by: exclusive
// footprint ids, with derived aggregate ids when the eraser was opted in.
func footprintIDSet(subjectID string, fp *SubjectFootprint, derived bool) []string {
	if derived {
		return SubjectFootprintIDsWithDerived(subjectID, fp)
	}
	return SubjectFootprintIDs(subjectID, fp)
}

// purgeByFootprint runs the footprint-aware purge (over ids) when the store offers one,
// else the legacy bare-subject-id purge, else reports Skipped. It never fails on absence.
// On the footprint path the shared footprint streams are reported as skipped.
func purgeByFootprint(ctx context.Context, name, subjectID string, fp *SubjectFootprint, ids []string,
	footprint func(context.Context, []string) (int64, error),
	legacy func(context.Context, string) (int64, error),
) (SubjectErasureOutcome, error) {
	out := SubjectErasureOutcome{Name: name}
	switch {
	case footprint != nil:
		n, err := footprint(ctx, ids)
		if err != nil {
			return out, err
		}
		out.Erased, out.FootprintAware = n, true
		out.SharedStreamsSkipped = sharedStreamCount(fp)
	case legacy != nil:
		n, err := legacy(ctx, subjectID)
		if err != nil {
			return out, err
		}
		out.Erased = n
	default:
		out.Skipped = true
	}
	return out, nil
}

// NewAuditSubjectEraser wraps an AuditStore as a SubjectErasable so DataEraser reaches
// the subject's audit trail (Article 17). The audit log records who/what/when in
// plaintext (actor, tenant, arbitrary metadata, raw error strings that can carry PII),
// which crypto-shredding the events does NOT touch.
//
// Two purge passes run, each only when the store supports it:
//
//   - SubjectAuditFootprintPurger (footprint-aware): deletes rows whose AggregateID is
//     any id in SubjectFootprintIDs — the bare subject id and the subject's EXCLUSIVE
//     stream ids (plus the aggregate ids derived from them under
//     WithDerivedAggregateIDs; the audit AggregateID is the raw aggregate id the
//     command targeted, e.g. "u1" for stream "User-u1", which only the derived set
//     matches). Streams shared with other subjects are skipped and reported in
//     SubjectErasureOutcome.SharedStreamsSkipped.
//   - SubjectAuditPurger (legacy): deletes rows whose Actor (or AggregateID) equals
//     the bare subject id, so rows the subject PERFORMED are reached too. Actor
//     matching needs this interface; a store offering only the footprint purger does
//     not reach actor rows.
//
// The footprint pass runs first, so a row matched by both (AggregateID equal to the
// subject id) is deleted — and counted — exactly once; Erased is the exact sum. When
// the store implements neither, EraseSubject reports Skipped (never fails). It also
// implements SubjectResidualCounter through SubjectAuditCounter (actor OR the same
// footprint ids). Register with the WithSubjectStore option of NewDataEraser.
func NewAuditSubjectEraser(store AuditStore) SubjectErasable {
	return &auditSubjectEraser{store: store}
}

type auditSubjectEraser struct {
	store   AuditStore
	derived bool
}

func (a *auditSubjectEraser) ErasableName() string { return "audit" }

func (a *auditSubjectEraser) withDerivedAggregateIDs() SubjectErasable {
	return &auditSubjectEraser{store: a.store, derived: true}
}

func (a *auditSubjectEraser) EraseSubject(ctx context.Context, subjectID string, fp *SubjectFootprint) (SubjectErasureOutcome, error) {
	out := SubjectErasureOutcome{Name: a.ErasableName()}
	fpPurger, fpOK := a.store.(SubjectAuditFootprintPurger)
	legacy, legacyOK := a.store.(SubjectAuditPurger)
	if !fpOK && !legacyOK {
		out.Skipped = true
		return out, nil
	}
	if fpOK {
		n, err := fpPurger.DeleteAuditByAggregateIDs(ctx, footprintIDSet(subjectID, fp, a.derived))
		if err != nil {
			return out, err
		}
		out.Erased += n
		out.FootprintAware = true
		out.SharedStreamsSkipped = sharedStreamCount(fp)
	}
	if legacyOK {
		// Runs AFTER the footprint pass: a row already removed above cannot be
		// matched again, so the two counts never overlap.
		n, err := legacy.DeleteAuditBySubject(ctx, subjectID)
		if err != nil {
			return out, err
		}
		out.Erased += n
	}
	return out, nil
}

// CountSubjectResidual counts audit rows whose Actor is the subject or whose
// AggregateID is any footprint id (the same set EraseSubject purged). Implements
// SubjectResidualCounter.
func (a *auditSubjectEraser) CountSubjectResidual(ctx context.Context, subjectID string, fp *SubjectFootprint) (int64, error) {
	counter, ok := a.store.(SubjectAuditCounter)
	if !ok {
		return 0, ErrResidualCountUnsupported
	}
	return counter.CountAuditBySubject(ctx, subjectID, footprintIDSet(subjectID, fp, a.derived))
}

// NewSagaSubjectEraser wraps a SagaStore as a SubjectErasable so DataEraser reaches the
// subject's saga state. Sagas copy correlation/business data out of events into their
// own plaintext state, which crypto-shredding does NOT touch. A saga's CorrelationID is
// whatever its correlation function derived — usually the subject's stream or aggregate
// id, not the bare subject id — so when the store implements
// SubjectSagaFootprintPurger, EraseSubject deletes sagas whose CorrelationID is any id
// in SubjectFootprintIDs (bare subject id + exclusive stream ids; aggregate ids derived
// from them only under WithDerivedAggregateIDs, since a bare correlation id such as
// "123" is not type-qualified); sagas correlated on a stream shared with other subjects
// are skipped and reported (SharedStreamsSkipped). Otherwise it falls back to
// SubjectSagaPurger (bare subject id); otherwise it reports Skipped. It also implements
// SubjectResidualCounter through SubjectSagaCounter over the same id set. Register with
// the WithSubjectStore option of NewDataEraser.
func NewSagaSubjectEraser(store SagaStore) SubjectErasable {
	return &sagaSubjectEraser{store: store}
}

type sagaSubjectEraser struct {
	store   SagaStore
	derived bool
}

func (s *sagaSubjectEraser) ErasableName() string { return "saga" }

func (s *sagaSubjectEraser) withDerivedAggregateIDs() SubjectErasable {
	return &sagaSubjectEraser{store: s.store, derived: true}
}

func (s *sagaSubjectEraser) EraseSubject(ctx context.Context, subjectID string, fp *SubjectFootprint) (SubjectErasureOutcome, error) {
	var footprint func(context.Context, []string) (int64, error)
	var legacy func(context.Context, string) (int64, error)
	if p, ok := s.store.(SubjectSagaFootprintPurger); ok {
		footprint = p.DeleteSagasByCorrelationIDs
	}
	if p, ok := s.store.(SubjectSagaPurger); ok {
		legacy = p.DeleteSagasBySubject
	}
	return purgeByFootprint(ctx, s.ErasableName(), subjectID, fp, footprintIDSet(subjectID, fp, s.derived), footprint, legacy)
}

// CountSubjectResidual counts sagas whose CorrelationID is any footprint id (the same
// set EraseSubject purged). Implements SubjectResidualCounter.
func (s *sagaSubjectEraser) CountSubjectResidual(ctx context.Context, subjectID string, fp *SubjectFootprint) (int64, error) {
	counter, ok := s.store.(SubjectSagaCounter)
	if !ok {
		return 0, ErrResidualCountUnsupported
	}
	return counter.CountSagasByCorrelationIDs(ctx, footprintIDSet(subjectID, fp, s.derived))
}

// NewOutboxSubjectEraser wraps an OutboxStore as a SubjectErasable so DataEraser reaches
// the subject's outbox rows. The default outbox path stores the ENCRYPTED payload (which
// crypto-shredding erases), but a route Transform that emits a decrypted/reshaped payload
// leaves an independent plaintext copy, and dead-lettered rows persist. The outbox's
// AggregateID is the producing STREAM id ("User-u1", never "u1"), so when the store
// implements SubjectOutboxFootprintPurger, EraseSubject deletes rows — of any status —
// whose AggregateID is any id in SubjectFootprintIDs (the subject's EXCLUSIVE footprint
// streams). Rows of a stream shared with other subjects are left in place (a co-tenant's
// pending, undelivered messages live there too) and reported in
// SubjectErasureOutcome.SharedStreamsSkipped. Otherwise it falls back to
// SubjectOutboxPurger (bare subject id, which library-written rows never match);
// otherwise it reports Skipped. It also implements SubjectResidualCounter through
// SubjectOutboxCounter over the same id set. Register with the WithSubjectStore option
// of NewDataEraser.
func NewOutboxSubjectEraser(store OutboxStore) SubjectErasable {
	return &outboxSubjectEraser{store: store}
}

type outboxSubjectEraser struct {
	store   OutboxStore
	derived bool
}

func (o *outboxSubjectEraser) ErasableName() string { return "outbox" }

func (o *outboxSubjectEraser) withDerivedAggregateIDs() SubjectErasable {
	return &outboxSubjectEraser{store: o.store, derived: true}
}

func (o *outboxSubjectEraser) EraseSubject(ctx context.Context, subjectID string, fp *SubjectFootprint) (SubjectErasureOutcome, error) {
	var footprint func(context.Context, []string) (int64, error)
	var legacy func(context.Context, string) (int64, error)
	if p, ok := o.store.(SubjectOutboxFootprintPurger); ok {
		footprint = p.DeleteOutboxByAggregateIDs
	}
	if p, ok := o.store.(SubjectOutboxPurger); ok {
		legacy = p.DeleteOutboxBySubject
	}
	return purgeByFootprint(ctx, o.ErasableName(), subjectID, fp, footprintIDSet(subjectID, fp, o.derived), footprint, legacy)
}

// CountSubjectResidual counts outbox rows whose AggregateID is any footprint id (the
// same set EraseSubject purged). Implements SubjectResidualCounter.
func (o *outboxSubjectEraser) CountSubjectResidual(ctx context.Context, subjectID string, fp *SubjectFootprint) (int64, error) {
	counter, ok := o.store.(SubjectOutboxCounter)
	if !ok {
		return 0, ErrResidualCountUnsupported
	}
	return counter.CountOutboxByAggregateIDs(ctx, footprintIDSet(subjectID, fp, o.derived))
}

// NewIdempotencySubjectEraser wraps an IdempotencyStore as a SubjectErasable so DataEraser
// reaches the subject's idempotency records. Records are TTL-bounded and keyed by a command
// hash, but the optional Response payload can hold PII. A record's AggregateID is the raw
// aggregate id the command affected, so when the store implements
// SubjectIdempotencyFootprintPurger, EraseSubject deletes records whose AggregateID is
// any id in SubjectFootprintIDs (bare subject id + exclusive stream ids; the derived
// aggregate ids that a raw AggregateID column actually holds only under
// WithDerivedAggregateIDs, because they are not type-qualified and deleting another
// aggregate's record would let a replayed command execute twice). Records keyed by a
// stream shared with other subjects are skipped and reported (SharedStreamsSkipped).
// Otherwise it falls back to SubjectIdempotencyPurger (bare subject id); otherwise it
// reports Skipped. It also implements SubjectResidualCounter through
// SubjectIdempotencyCounter over the same id set. Register with the WithSubjectStore
// option of NewDataEraser.
func NewIdempotencySubjectEraser(store IdempotencyStore) SubjectErasable {
	return &idempotencySubjectEraser{store: store}
}

type idempotencySubjectEraser struct {
	store   IdempotencyStore
	derived bool
}

func (i *idempotencySubjectEraser) ErasableName() string { return "idempotency" }

func (i *idempotencySubjectEraser) withDerivedAggregateIDs() SubjectErasable {
	return &idempotencySubjectEraser{store: i.store, derived: true}
}

func (i *idempotencySubjectEraser) EraseSubject(ctx context.Context, subjectID string, fp *SubjectFootprint) (SubjectErasureOutcome, error) {
	var footprint func(context.Context, []string) (int64, error)
	var legacy func(context.Context, string) (int64, error)
	if p, ok := i.store.(SubjectIdempotencyFootprintPurger); ok {
		footprint = p.DeleteIdempotencyByAggregateIDs
	}
	if p, ok := i.store.(SubjectIdempotencyPurger); ok {
		legacy = p.DeleteIdempotencyBySubject
	}
	return purgeByFootprint(ctx, i.ErasableName(), subjectID, fp, footprintIDSet(subjectID, fp, i.derived), footprint, legacy)
}

// CountSubjectResidual counts idempotency records whose AggregateID is any footprint
// id (the same set EraseSubject purged). Implements SubjectResidualCounter.
func (i *idempotencySubjectEraser) CountSubjectResidual(ctx context.Context, subjectID string, fp *SubjectFootprint) (int64, error) {
	counter, ok := i.store.(SubjectIdempotencyCounter)
	if !ok {
		return 0, ErrResidualCountUnsupported
	}
	return counter.CountIdempotencyByAggregateIDs(ctx, footprintIDSet(subjectID, fp, i.derived))
}

// NewSnapshotSubjectEraser wraps a SnapshotAdapter as a SubjectErasable that deletes the
// snapshot of each stream in the subject's resolved footprint. Snapshots serialize
// decrypted aggregate STATE in plaintext, which crypto-shredding does NOT touch, so an
// un-deleted snapshot leaves the subject's PII recoverable. Snapshots of streams shared
// with other subjects are deleted too (SharedStreamsSkipped stays 0): a snapshot is a
// rebuildable cache, so deleting it loses nothing for the co-tenants while keeping it
// would preserve the subject's plaintext state. DeleteSnapshot is idempotent, so Erased
// counts the footprint streams whose snapshot was cleared. It also implements
// SubjectResidualCounter: a footprint stream that still has a snapshot counts as one
// residual row (one LoadSnapshot per footprint stream). Register with the
// WithSubjectStore option of NewDataEraser.
func NewSnapshotSubjectEraser(adapter adapters.SnapshotAdapter) SubjectErasable {
	return &snapshotSubjectEraser{adapter: adapter}
}

type snapshotSubjectEraser struct{ adapter adapters.SnapshotAdapter }

func (s *snapshotSubjectEraser) ErasableName() string { return "snapshot" }

func (s *snapshotSubjectEraser) EraseSubject(ctx context.Context, _ string, fp *SubjectFootprint) (SubjectErasureOutcome, error) {
	// Snapshots are keyed by stream, so this eraser is footprint-driven by nature.
	out := SubjectErasureOutcome{Name: s.ErasableName(), FootprintAware: true}
	if fp == nil || len(fp.Streams) == 0 {
		return out, nil
	}
	var firstErr error
	for _, streamID := range fp.Streams {
		if err := s.adapter.DeleteSnapshot(ctx, streamID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out.Erased++
	}
	return out, firstErr
}

// CountSubjectResidual counts the footprint streams that still have a snapshot.
// Implements SubjectResidualCounter.
func (s *snapshotSubjectEraser) CountSubjectResidual(ctx context.Context, _ string, fp *SubjectFootprint) (int64, error) {
	if fp == nil || len(fp.Streams) == 0 {
		return 0, nil
	}
	var n int64
	for _, streamID := range fp.Streams {
		snap, err := s.adapter.LoadSnapshot(ctx, streamID)
		if err != nil {
			return n, err
		}
		if snap != nil {
			n++
		}
	}
	return n, nil
}
