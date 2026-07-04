package mink

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go-mink.dev/adapters"
)

// RetentionAction is the action a RetentionPolicy applies to matched events.
type RetentionAction int

const (
	// ActionShred crypto-shreds matched events by revoking their encryption key.
	// Append-only-safe and unrecoverable. The primary, GDPR-meaningful action.
	ActionShred RetentionAction = iota

	// ActionRedactFields redacts the policy's Fields on matched events. Because the
	// event store is append-only, the redaction is applied through the policy's Apply
	// hook (typically to read models / external stores), not by mutating event rows.
	ActionRedactFields

	// ActionAnonymize pseudonymizes the policy's Fields on matched events, also via
	// the policy's Apply hook (see anonymizer in pii-anonymization).
	ActionAnonymize
)

// String returns the action name.
func (a RetentionAction) String() string {
	switch a {
	case ActionShred:
		return "Shred"
	case ActionRedactFields:
		return "RedactFields"
	case ActionAnonymize:
		return "Anonymize"
	default:
		return "Unknown"
	}
}

// RetentionPolicy describes a retention rule: a matcher (all set fields must match,
// AND) and an action. Policies are composable.
type RetentionPolicy struct {
	Name string

	// Matchers — any left zero is ignored.
	Category     string        // stream category (text before the first "-")
	StreamPrefix string        // stream-id prefix
	EventTypes   []string      // any-of event types
	TenantID     string        // metadata tenant id
	MaxAge       time.Duration // matches events older than MaxAge (0 = no age bound)

	Action RetentionAction
	Fields []string // fields for RedactFields/Anonymize (applied by Apply)

	// Apply performs RedactFields/Anonymize for a matched event. go-mink cannot mutate
	// append-only rows, so the caller applies the transform to its read models /
	// external stores. Ignored for ActionShred; a Redact/Anonymize policy without
	// Apply leaves matched events unhandled (reported as Skipped, never silent).
	Apply func(ctx context.Context, e StoredEvent) error
}

// Validate reports a configuration error that would make the policy silently do
// nothing: a RedactFields or Anonymize policy with no Apply hook. go-mink cannot mutate
// append-only event rows, so those actions MUST be carried out against read models /
// external stores via Apply — without it, every match is skipped and no anonymization
// happens even though the sweep "succeeds". RetentionManager surfaces this on every
// Apply/DryRun so it can never pass unnoticed.
func (p RetentionPolicy) Validate() error {
	if (p.Action == ActionRedactFields || p.Action == ActionAnonymize) && p.Apply == nil {
		return fmt.Errorf("mink: retention policy %q uses %s but has no Apply hook — it would silently skip every match (go-mink cannot mutate append-only rows; provide Apply to redact/anonymize read models or external stores)", p.Name, p.Action)
	}
	return nil
}

// matchesStatic reports whether se satisfies the policy's time-independent matchers
// (category / stream prefix / tenant / event type). These never change as an event ages, so
// an event that fails them can never match this policy on any future sweep — which is what
// lets the resume frontier treat such events as permanently settled.
func (p RetentionPolicy) matchesStatic(se StoredEvent) bool {
	if p.Category != "" && streamCategory(se.StreamID) != p.Category {
		return false
	}
	if p.StreamPrefix != "" && !strings.HasPrefix(se.StreamID, p.StreamPrefix) {
		return false
	}
	if p.TenantID != "" && se.Metadata.TenantID != p.TenantID {
		return false
	}
	if len(p.EventTypes) > 0 && !containsString(p.EventTypes, se.Type) {
		return false
	}
	return true
}

// ageEligible reports whether se is old enough for the policy's MaxAge (always true when
// MaxAge is 0, i.e. no age bound). Unlike the static matchers this flips from false to true
// as wall-clock time advances — an event that statically matches a policy but is not yet
// ageEligible is "pending" and must not be skipped by advancing the frontier past it.
func (p RetentionPolicy) ageEligible(se StoredEvent, now time.Time) bool {
	return p.MaxAge <= 0 || now.Sub(se.Timestamp) >= p.MaxAge
}

func streamCategory(streamID string) string {
	if i := strings.IndexByte(streamID, '-'); i > 0 {
		return streamID[:i]
	}
	return streamID
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// RetentionReport summarizes a retention sweep.
type RetentionReport struct {
	DryRun  bool
	Scanned int // events examined
	// Truncated is true when WithRetentionMaxScan stopped this sweep at the per-run scan cap.
	// Not an error: the checkpoint has advanced and the next run resumes from there — if the
	// cap happened to land exactly on the head of the store, that next run simply finds
	// nothing new. Only ever true when a checkpoint is configured.
	Truncated   bool
	Matched     int      // (policy, event) matches
	Acted       int      // matches acted on (shred-with-key or applied hook)
	Skipped     int      // matches with no applicable handler (residual; e.g. Redact w/o Apply)
	KeysRevoked []string // distinct keys crypto-shredded (sorted)
	Errors      []error  // non-fatal per-action errors (incl. loud policy-misconfig errors)
}

// Failed reports whether the sweep had any error — a per-action failure or a
// misconfigured policy (e.g. RedactFields/Anonymize with no Apply hook). A caller
// SHOULD check it: a "successful" (nil-error) Apply can still have skipped everything.
func (r *RetentionReport) Failed() bool {
	return len(r.Errors) > 0
}

// RetentionManager applies retention policies over the event store. It NEVER deletes or
// mutates event rows — Shred revokes keys, Redact/Anonymize delegate to the policy Apply
// hook — preserving the append-only log.
//
// Scheduling is the caller's responsibility: Apply performs a single sweep and returns.
// go-mink does NOT run it on a timer — wire Apply to your own scheduler (cron, gocron,
// a ticker) at whatever cadence your retention SLA requires. "Sweep" here means one pass,
// not a self-scheduling loop.
type RetentionManager struct {
	store      *EventStore
	policies   []RetentionPolicy
	batchSize  int
	now        func() time.Time
	checkpoint *retentionCheckpoint // nil ⇒ scan the whole store every run (default)
	maxScan    int                  // >0 ⇒ stop a single sweep after this many scanned events
}

// retentionCheckpoint persists the safe-resume frontier between sweeps so a scheduled Apply
// resumes from where the previous one settled instead of re-scanning the whole store.
type retentionCheckpoint struct {
	store CheckpointStore
	name  string
}

// RetentionManagerOption configures a RetentionManager.
type RetentionManagerOption func(*RetentionManager)

// WithRetentionBatchSize sets the scan batch size (default 1000).
func WithRetentionBatchSize(size int) RetentionManagerOption {
	return func(m *RetentionManager) {
		if size > 0 {
			m.batchSize = size
		}
	}
}

// WithRetentionClock overrides the clock used for MaxAge evaluation (for testing).
func WithRetentionClock(now func() time.Time) RetentionManagerOption {
	return func(m *RetentionManager) {
		if now != nil {
			m.now = now
		}
	}
}

// WithRetentionCheckpoint makes sweeps resumable (mirroring the projection engine's
// WithCheckpointStore). When configured, Apply starts its scan from the position persisted
// under name — via the same CheckpointStore projections use — instead of position 0, and
// after acting persists a safe-resume frontier: the highest global position below which no
// event can newly match a policy on a future run. This bounds a scheduled sweep's
// steady-state cost to the events within the retention window rather than the whole,
// ever-growing store. Absent this option, Apply scans from 0 and persists nothing —
// exactly as before.
//
// name shares the CheckpointStore keyspace with projection checkpoints, so it MUST NOT
// collide with a projection name; use a reserved sentinel such as "__mink_retention__".
//
// The persisted frontier is valid only for the current policy set's STATIC matchers
// (category / stream prefix / event type / tenant). Changing a policy's MaxAge is safe, but
// broadening a static matcher so it now covers older events already scanned past requires
// resetting the checkpoint (CheckpointStore.DeleteCheckpoint) or a fresh name — the same
// rule as rebuilding a projection after changing its logic.
//
// A nil store or empty name is ignored (leaves the manager in its default full-scan mode).
func WithRetentionCheckpoint(store CheckpointStore, name string) RetentionManagerOption {
	return func(m *RetentionManager) {
		if store != nil && name != "" {
			m.checkpoint = &retentionCheckpoint{store: store, name: name}
		}
	}
}

// WithRetentionMaxScan bounds a single sweep to at most n scanned events (n <= 0 ⇒
// unbounded, the default). It exists to bound the FIRST sweep after enabling retention on
// an already-large store — which WithRetentionCheckpoint alone cannot, since the first run
// has no prior frontier and must reach the aged tail once. The remainder is resumed on the
// next run via the checkpoint, so the cap is only meaningful together with
// WithRetentionCheckpoint; configured without one it is reported (non-fatally) as
// ErrRetentionMaxScanNeedsCheckpoint in RetentionReport.Errors and the sweep runs unbounded
// rather than silently capping and never reaching the tail. A capped run sets
// RetentionReport.Truncated.
func WithRetentionMaxScan(n int) RetentionManagerOption {
	return func(m *RetentionManager) {
		if n > 0 {
			m.maxScan = n
		}
	}
}

// NewRetentionManager creates a manager for the given store and policies.
func NewRetentionManager(store *EventStore, policies []RetentionPolicy, opts ...RetentionManagerOption) *RetentionManager {
	m := &RetentionManager{store: store, policies: policies, batchSize: 1000, now: time.Now}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Apply enforces the configured policies and returns a report.
func (m *RetentionManager) Apply(ctx context.Context) (*RetentionReport, error) {
	return m.run(ctx, false)
}

// DryRun reports what Apply would do without making any change.
func (m *RetentionManager) DryRun(ctx context.Context) (*RetentionReport, error) {
	return m.run(ctx, true)
}

// Validate returns any policy misconfigurations (e.g. a RedactFields/Anonymize policy
// with no Apply hook) so a caller can fail fast at startup instead of discovering it in
// a report. Apply and DryRun also surface these on every run.
func (m *RetentionManager) Validate() []error {
	var errs []error
	for i := range m.policies {
		if err := m.policies[i].Validate(); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func (m *RetentionManager) run(ctx context.Context, dryRun bool) (*RetentionReport, error) {
	if _, ok := m.store.Adapter().(adapters.SubscriptionAdapter); !ok {
		return nil, ErrExportScanNotSupported
	}
	report := &RetentionReport{DryRun: dryRun}
	// Fail loud on policies that can never act (a RedactFields/Anonymize policy without
	// an Apply hook). Surfaced on every Apply AND DryRun via report.Errors (so Failed()
	// is true), rather than a silent Skipped count you'd think you anonymized when you
	// did not.
	for i := range m.policies {
		if err := m.policies[i].Validate(); err != nil {
			report.Errors = append(report.Errors, err)
		}
	}
	// A per-run scan cap needs a checkpoint to resume the remainder on the next run; without
	// one it would re-scan the same oldest events every run and never reach the aged tail.
	// Treat that as a loud misconfiguration and scan unbounded rather than cap-and-forget.
	maxScan := m.maxScan
	if maxScan > 0 && m.checkpoint == nil {
		report.Errors = append(report.Errors, ErrRetentionMaxScanNeedsCheckpoint)
		maxScan = 0
	}

	// Resume from the persisted frontier when a checkpoint is configured; otherwise scan the
	// whole store from position 0 (the unchanged default).
	var startPos uint64
	if m.checkpoint != nil {
		cp, err := m.checkpoint.store.GetCheckpoint(ctx, m.checkpoint.name)
		if err != nil {
			return nil, fmt.Errorf("mink: retention read checkpoint %q: %w", m.checkpoint.name, err)
		}
		startPos = cp
	}

	now := m.now()
	shredKeys := map[string]struct{}{}

	// frontier is the highest position below which every event is settled — already acted on
	// or matching no policy — and so can never newly match on a future run. It advances only
	// through the maximal contiguous prefix of non-pending events from startPos and freezes
	// at the first pending one (statically matches a policy but is not yet ageEligible). The
	// run still scans and acts past the freeze; only the persisted resume point is held back.
	// This is correct without assuming timestamps track global position: age-matching is
	// monotonic in wall-clock time, so a non-pending event stays settled on every later run.
	frontier := startPos
	frozen := false

	position := startPos
scan:
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := m.store.LoadEventsFromPosition(ctx, position, m.batchSize)
		if err != nil {
			return nil, fmt.Errorf("mink: retention scan from %d: %w", position, err)
		}
		if len(batch) == 0 {
			break
		}
		for _, se := range batch {
			report.Scanned++
			pending := false
			for i := range m.policies {
				p := m.policies[i]
				if !p.matchesStatic(se) {
					continue
				}
				if p.ageEligible(se, now) {
					report.Matched++
					if !dryRun {
						m.act(ctx, p, se, shredKeys, report)
					}
				} else {
					// Statically matches but too young — it will match a future run, so the
					// frontier must not advance past it.
					pending = true
				}
			}
			if !frozen {
				if pending {
					frozen = true
				} else {
					frontier = se.GlobalPosition
				}
			}
			position = se.GlobalPosition
			// Only truncate once the frontier has advanced past where we resumed, so the
			// next run is guaranteed to make forward progress. If a pending event has frozen
			// the frontier at startPos, truncating here would persist nothing and re-scan
			// this same window every run — permanently starving aged events beyond the cap
			// when timestamps are not monotonic in global position (which the frontier is
			// explicitly designed to tolerate). In that case fall through and scan to HEAD,
			// matching the unbounded behavior, until the boundary event ages.
			if maxScan > 0 && report.Scanned >= maxScan && frontier > startPos {
				report.Truncated = true
				break scan
			}
		}
		if len(batch) < m.batchSize {
			break
		}
	}

	if !dryRun && len(shredKeys) > 0 {
		m.revoke(shredKeys, report)
	}

	// Persist the advanced frontier so the next sweep resumes here. Apply only (DryRun must
	// change nothing) and only when it advanced (skip no-op writes). A write failure is
	// non-fatal — the sweep did its work; the next run just redoes the range harmlessly.
	if m.checkpoint != nil && !dryRun && frontier > startPos {
		if err := m.checkpoint.store.SetCheckpoint(ctx, m.checkpoint.name, frontier); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("mink: retention write checkpoint %q: %w", m.checkpoint.name, err))
		}
	}
	return report, nil
}

func (m *RetentionManager) act(ctx context.Context, p RetentionPolicy, se StoredEvent, shredKeys map[string]struct{}, report *RetentionReport) {
	switch p.Action {
	case ActionShred:
		if k := GetEncryptionKeyID(se.Metadata); k != "" {
			shredKeys[k] = struct{}{}
			report.Acted++
		} else {
			report.Skipped++ // nothing encrypted to shred
		}
	case ActionRedactFields, ActionAnonymize:
		if p.Apply == nil {
			// No handler — residual. run() has already surfaced this as a loud
			// report error (see Validate); the Skipped count is informational.
			report.Skipped++
			return
		}
		if err := p.Apply(ctx, se); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("policy %q apply: %w", p.Name, err))
			return
		}
		report.Acted++
	}
}

func (m *RetentionManager) revoke(shredKeys map[string]struct{}, report *RetentionReport) {
	cfg := m.store.EncryptionConfig()
	if cfg == nil {
		report.Errors = append(report.Errors, ErrErasureNotConfigured)
		return
	}
	for k := range shredKeys {
		if err := cfg.RevokeKey(k); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("revoke key %q: %w", k, err))
			continue
		}
		report.KeysRevoked = append(report.KeysRevoked, k)
	}
	sort.Strings(report.KeysRevoked)
}
