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
//
// At least one matcher MUST be set (see Validate): a policy with none would match every
// event in the store, which for ActionShred means a whole-store crypto-shred.
type RetentionPolicy struct {
	Name string

	// Matchers — any left zero is ignored, but at least one must be set.
	Category string // stream category (text before the first "-")
	// StreamPrefix matches stream ids by plain prefix, so "user-1" also matches
	// "user-10" and "user-123". To scope a policy to one aggregate id end the prefix with
	// the id separator ("user-1-"), or use Category for a whole category.
	StreamPrefix string
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
// nothing — or far too much:
//
//   - A policy with no matcher at all (Category, StreamPrefix, EventTypes and TenantID
//     empty and MaxAge zero) matches EVERY event. For ActionShred that is a whole-store
//     crypto-shred, so it is rejected for every action with ErrRetentionUnscopedPolicy.
//     RetentionManager additionally leaves such a policy inert (it never matches or acts)
//     while reporting the error on every Apply/DryRun.
//   - A RedactFields or Anonymize policy with no Apply hook. go-mink cannot mutate
//     append-only event rows, so those actions MUST be carried out against read models /
//     external stores via Apply — without it, every match is skipped and no anonymization
//     happens even though the sweep "succeeds".
//
// RetentionManager surfaces both on every Apply/DryRun so they can never pass unnoticed.
func (p RetentionPolicy) Validate() error {
	if p.unscoped() {
		return fmt.Errorf("mink: retention policy %q: %w", p.Name, ErrRetentionUnscopedPolicy)
	}
	if (p.Action == ActionRedactFields || p.Action == ActionAnonymize) && p.Apply == nil {
		return fmt.Errorf("mink: retention policy %q uses %s but has no Apply hook — it would silently skip every match (go-mink cannot mutate append-only rows; provide Apply to redact/anonymize read models or external stores)", p.Name, p.Action)
	}
	return nil
}

// unscoped reports whether the policy has no matcher at all and would therefore match
// every event in the store.
func (p RetentionPolicy) unscoped() bool {
	return p.Category == "" && p.StreamPrefix == "" && len(p.EventTypes) == 0 &&
		p.TenantID == "" && p.MaxAge <= 0
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
	// nothing new. Only ever true when a checkpoint is configured. A sweep never truncates
	// while its resume point could not advance (the first unsettled event is pending, or a
	// Shred match whose key is still to be decided by the guard); it scans to HEAD instead.
	Truncated bool
	Matched   int // (policy, event) matches
	// Acted counts matches acted on: Shred matches whose key was actually revoked in this
	// sweep (KeysRevoked), plus RedactFields/Anonymize matches whose Apply hook ran. A
	// Shred match is NOT acted on while its key is refused by the shared-key guard, fails
	// to revoke, or cannot be revoked (no encryption config) — those count as Skipped and
	// are re-swept by the next run (see WithRetentionCheckpoint). Always 0 on DryRun.
	Acted int
	// Skipped counts matches that remain unhandled after the sweep (residual): a
	// RedactFields/Anonymize match with no Apply hook, a Shred match with no encryption
	// envelope (see UnencryptedMatches), and a Shred match whose key the shared-key guard
	// refused, failed to revoke or could not be revoked. Always 0 on DryRun.
	Skipped int
	// UnencryptedMatches counts ActionShred matches that carried no field-encryption
	// envelope and so could not be crypto-shredded — they remain in plaintext. Counted on
	// both Apply and DryRun; when > 0 the report also carries ErrRetentionUnencryptedMatches
	// so a shred sweep never looks fully successful while matched plaintext remains.
	UnencryptedMatches int
	// KeysToRevoke lists the distinct keys (sorted) collected by Shred policies that passed
	// the shared-key guard. On DryRun it previews exactly which keys Apply would revoke; on
	// Apply it is the set revocation was attempted for (KeysRevoked holds the successes).
	KeysToRevoke []string
	// SharedKeysSkipped lists the distinct keys (sorted) the shared-key blast-radius guard
	// refused to revoke because they also protect events outside the Shred policies'
	// scope. Each has a RetentionSharedKeyError in Errors. Always empty when the guard is
	// disabled with WithAllowSharedKeyRevocation.
	SharedKeysSkipped []string
	KeysRevoked       []string // distinct keys crypto-shredded (sorted)
	Errors            []error  // non-fatal per-action errors (incl. loud policy-misconfig errors)
}

// Failed reports whether the sweep had any error — a per-action failure, a
// misconfigured policy (e.g. RedactFields/Anonymize with no Apply hook, or a policy with
// no matchers), a key the shared-key guard refused to revoke, or matched plaintext a
// Shred policy could not erase. A caller SHOULD check it: a "successful" (nil-error)
// Apply can still have skipped everything.
func (r *RetentionReport) Failed() bool {
	return len(r.Errors) > 0
}

// RetentionManager applies retention policies over the event store. It NEVER deletes or
// mutates event rows — Shred revokes keys, Redact/Anonymize delegate to the policy Apply
// hook — preserving the append-only log.
//
// Crypto-shredding revokes a MASTER key, which erases every event encrypted under it —
// not only the events a policy matched. By default a shared-key blast-radius guard
// therefore verifies, before any (irreversible) revocation, that every event encrypted
// under a candidate key is covered by a Shred policy in this sweep; a key that also
// protects out-of-scope events is skipped and reported (RetentionReport.SharedKeysSkipped,
// ErrRetentionSharedKey). See WithAllowSharedKeyRevocation to disable the guard.
//
// Scheduling is the caller's responsibility: Apply performs a single sweep and returns.
// go-mink does NOT run it on a timer — wire Apply to your own scheduler (cron, gocron,
// a ticker) at whatever cadence your retention SLA requires. "Sweep" here means one pass,
// not a self-scheduling loop.
type RetentionManager struct {
	store          *EventStore
	policies       []RetentionPolicy
	batchSize      int
	now            func() time.Time
	checkpoint     *retentionCheckpoint // nil ⇒ scan the whole store every run (default)
	maxScan        int                  // >0 ⇒ stop a single sweep after this many scanned events
	allowSharedKey bool                 // true ⇒ shared-key blast-radius guard disabled
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
// The shared-key guard (see RetentionManager) is unaffected by the checkpoint: it always
// verifies candidate keys against the WHOLE store, since events behind the frontier may
// share a key with the ones matched in this run. The two compose safely: a Shred match
// whose key was NOT revoked in this sweep — refused by the guard (SharedKeysSkipped), a
// failed RevokeKey, or no encryption config — is treated like a pending event. The
// persisted frontier is held back to just before the first such match, so those events
// are re-scanned, re-matched and re-reported on every later run until the key is
// revoked (after WithAllowSharedKeyRevocation, a covering Shred policy, or a key split);
// they are never silently settled behind the checkpoint.
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
//
// The cap only ever stops a sweep that is guaranteed to persist progress. A run whose
// resume point cannot advance past the cap — the first unsettled event is pending (not yet
// aged) or a Shred match whose key the guard has yet to decide — scans to HEAD instead,
// exactly as without the cap; otherwise a refused key or a pending event at the resume
// point would re-scan the same window every run and starve the aged tail. The shared-key
// guard's own verification scan is never capped (it must see the whole store).
func WithRetentionMaxScan(n int) RetentionManagerOption {
	return func(m *RetentionManager) {
		if n > 0 {
			m.maxScan = n
		}
	}
}

// WithAllowSharedKeyRevocation DISABLES the shared-key blast-radius guard, letting an
// ActionShred policy revoke a key even when that key also protects events outside the
// policy scope — crypto-shredding those events too, permanently.
//
// This is dangerous. With a single default key, or per-tenant keys, revoking a key erases
// every event encrypted under it, regardless of Category/StreamPrefix/EventTypes/TenantID
// or MaxAge. Use it only when you have confirmed (e.g. via DryRun's SharedKeysSkipped)
// that the whole blast radius is acceptable. The safe alternative is to give each retention
// scope its own key (WithSubjectKeyResolver / WithTenantKeyResolver). When set, the guard's
// verification scan is skipped entirely.
//
// With WithRetentionCheckpoint, a sweep that refused a key left its matches unsettled
// (the persisted frontier stops just before the first refused match). Re-running with this
// option — or after adding a Shred policy that covers the key's out-of-scope events —
// resumes from there, re-matches those events and revokes the key; no checkpoint reset is
// needed.
func WithAllowSharedKeyRevocation() RetentionManagerOption {
	return func(m *RetentionManager) { m.allowSharedKey = true }
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

// DryRun reports what Apply would do without making any change: matches, the keys Apply
// would revoke (KeysToRevoke), the keys the shared-key guard would refuse
// (SharedKeysSkipped) and matched plaintext a Shred could not erase (UnencryptedMatches).
// It lets an operator preview a sweep's blast radius before revoking anything.
func (m *RetentionManager) DryRun(ctx context.Context) (*RetentionReport, error) {
	return m.run(ctx, true)
}

// Validate returns any policy misconfigurations (e.g. a RedactFields/Anonymize policy
// with no Apply hook, or a policy with no matchers) so a caller can fail fast at startup
// instead of discovering it in a report. Apply and DryRun also surface these on every run.
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
	// an Apply hook) or would act on everything (no matchers). Surfaced on every Apply AND
	// DryRun via report.Errors (so Failed() is true), rather than a silent Skipped count
	// you'd think you anonymized when you did not. An unscoped policy is additionally left
	// out of the sweep: reporting a whole-store shred after the fact would be no guard.
	active := make([]RetentionPolicy, 0, len(m.policies))
	for i := range m.policies {
		if err := m.policies[i].Validate(); err != nil {
			report.Errors = append(report.Errors, err)
		}
		if m.policies[i].unscoped() {
			continue
		}
		active = append(active, m.policies[i])
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
	shred := newShredCandidates()

	// frontier is the highest position below which every event is settled — already acted on
	// or matching no policy — and so can never newly match on a future run. It advances only
	// through the maximal contiguous prefix of non-pending events from startPos and freezes
	// at the first pending one (statically matches a policy but is not yet ageEligible). The
	// run still scans and acts past the freeze; only the persisted resume point is held back.
	// This is correct without assuming timestamps track global position: age-matching is
	// monotonic in wall-clock time, so a non-pending event stays settled on every later run.
	//
	// A Shred match is only provisionally settled: whether it was acted on is decided after
	// the scan, by the shared-key guard and the revoke. Every candidate key therefore
	// remembers the frontier just before its first match (shred.frontier) so that, should
	// the key end up unrevoked, the frontier can be clamped back there and the match is
	// re-swept next run instead of being settled behind the checkpoint.
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
			for i := range active {
				p := active[i]
				if !p.matchesStatic(se) {
					continue
				}
				if p.ageEligible(se, now) {
					report.Matched++
					m.act(ctx, p, se, shred, frontier, report, dryRun)
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
			// Only truncate once the resume point is guaranteed to advance past where we
			// resumed, so the next run makes forward progress. "Guaranteed" treats every
			// Shred match collected so far as unsettled (its key may yet be refused or fail
			// to revoke, which clamps the frontier back before it). If a pending event, or
			// an undecided Shred match, holds the resume point at startPos, truncating here
			// could persist nothing and re-scan this same window every run — permanently
			// starving aged events beyond the cap. In that case fall through and scan to
			// HEAD, matching the unbounded behavior, until the boundary event settles.
			if maxScan > 0 && report.Scanned >= maxScan && shred.settled(frontier) > startPos {
				report.Truncated = true
				break scan
			}
		}
		if len(batch) < m.batchSize {
			break
		}
	}

	// Matched plaintext can never be crypto-shredded: say so, loudly, on every run.
	if report.UnencryptedMatches > 0 {
		report.Errors = append(report.Errors, fmt.Errorf("%w: %d matched event(s)",
			ErrRetentionUnencryptedMatches, report.UnencryptedMatches))
	}

	if len(shred.matches) > 0 {
		if err := m.guardSharedKeys(ctx, shred.keys(), active, now, report); err != nil {
			return nil, err
		}
		if !dryRun {
			m.revoke(report)
			// Settle the Shred accounting now that the key decisions are known: a match is
			// acted on iff its key was revoked in this sweep; otherwise it is a residual
			// (Skipped) and its position must not be settled behind the checkpoint.
			revoked := make(map[string]struct{}, len(report.KeysRevoked))
			for _, k := range report.KeysRevoked {
				revoked[k] = struct{}{}
			}
			for k, n := range shred.matches {
				if _, ok := revoked[k]; ok {
					report.Acted += n
					continue
				}
				report.Skipped += n
				if f := shred.frontier[k]; f < frontier {
					frontier = f
				}
			}
		}
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

// shredCandidates accumulates, during the scan, the keys an ActionShred policy wants
// revoked: how many enveloped matches each key covers (settled into Acted or Skipped once
// the guard and the revoke have decided the key's fate) and the resume frontier just
// before each key's first match (where the frontier is clamped back to if the key ends up
// unrevoked, so the match is re-swept next run). Keys are collected in scan order, so the
// first collected key has the lowest frontier.
type shredCandidates struct {
	matches  map[string]int
	frontier map[string]uint64
	floor    uint64 // frontier before the earliest collected match
	any      bool
}

func newShredCandidates() *shredCandidates {
	return &shredCandidates{matches: map[string]int{}, frontier: map[string]uint64{}}
}

// add records one enveloped Shred match under keyID, seen while the resume frontier
// stood at frontier (i.e. before this event could advance it).
func (c *shredCandidates) add(keyID string, frontier uint64) {
	if _, seen := c.frontier[keyID]; !seen {
		c.frontier[keyID] = frontier
		if !c.any || frontier < c.floor {
			c.floor = frontier
		}
		c.any = true
	}
	c.matches[keyID]++
}

// keys returns the candidate key set in the shape guardSharedKeys expects.
func (c *shredCandidates) keys() map[string]struct{} {
	out := make(map[string]struct{}, len(c.matches))
	for k := range c.matches {
		out[k] = struct{}{}
	}
	return out
}

// settled returns the resume point that is guaranteed whatever the guard decides: the
// current frontier, held back to just before the earliest Shred match collected so far.
func (c *shredCandidates) settled(frontier uint64) uint64 {
	if c.any && c.floor < frontier {
		return c.floor
	}
	return frontier
}

// act handles one (policy, event) match. On a dry run it only classifies the match —
// collecting the key a Shred would revoke and counting unencrypted matches — without
// running Apply hooks or touching the Acted/Skipped counters. frontier is the resume
// frontier as it stood before this event (see shredCandidates).
func (m *RetentionManager) act(ctx context.Context, p RetentionPolicy, se StoredEvent, shred *shredCandidates, frontier uint64, report *RetentionReport, dryRun bool) {
	switch p.Action {
	case ActionShred:
		// Only a complete envelope (fields + key id + wrapped DEK) is ciphertext a key
		// revocation erases. A bare key id is plaintext as far as shredding is concerned.
		if !HasEncryptionEnvelope(se.Metadata) {
			report.UnencryptedMatches++
			if !dryRun {
				report.Skipped++ // nothing encrypted to shred
			}
			return
		}
		// Acted/Skipped for this match are settled after the scan, once the guard and the
		// revoke have decided the key's fate (run()).
		shred.add(GetEncryptionKeyID(se.Metadata), frontier)
	case ActionRedactFields, ActionAnonymize:
		if dryRun {
			return
		}
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

// guardSharedKeys splits the candidate keys into KeysToRevoke and SharedKeysSkipped. With
// the blast-radius guard enabled (the default) it scans the WHOLE store — from position 0,
// regardless of any resume checkpoint — and treats a key as shared when ANY event
// encrypted under it is not covered by a Shred policy in this sweep (fails the static
// matchers, or is not yet ageEligible at sweep time): revoking it would crypto-shred that
// event too. Each shared key gets a RetentionSharedKeyError in report.Errors so the sweep
// is visibly incomplete. With WithAllowSharedKeyRevocation every candidate is revocable and
// no scan is made. A scan failure is fatal: exclusivity cannot be proven, so nothing may be
// revoked.
func (m *RetentionManager) guardSharedKeys(ctx context.Context, candidates map[string]struct{}, policies []RetentionPolicy, now time.Time, report *RetentionReport) error {
	outOfScope := map[string]int{}
	if !m.allowSharedKey {
		var err error
		outOfScope, err = m.detectSharedShredKeys(ctx, candidates, policies, now)
		if err != nil {
			return err
		}
	}
	for k := range candidates {
		if n, shared := outOfScope[k]; shared {
			report.SharedKeysSkipped = append(report.SharedKeysSkipped, k)
			report.Errors = append(report.Errors, &RetentionSharedKeyError{KeyID: k, OutOfScope: n})
			continue
		}
		report.KeysToRevoke = append(report.KeysToRevoke, k)
	}
	sort.Strings(report.KeysToRevoke)
	sort.Strings(report.SharedKeysSkipped)
	return nil
}

// detectSharedShredKeys scans the whole store and returns, for each candidate key that
// also protects events no Shred policy covers, the number of such out-of-scope events.
// Keys absent from the result are exclusive to the sweep's scope. It mirrors the
// DataEraser's shared-key check, with "covered by a Shred policy" in place of "tagged for
// the subject".
func (m *RetentionManager) detectSharedShredKeys(ctx context.Context, candidates map[string]struct{}, policies []RetentionPolicy, now time.Time) (map[string]int, error) {
	outOfScope := map[string]int{}
	var position uint64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := m.store.LoadEventsFromPosition(ctx, position, m.batchSize)
		if err != nil {
			return nil, fmt.Errorf("mink: retention shared-key scan from %d: %w", position, err)
		}
		if len(batch) == 0 {
			break
		}
		for _, se := range batch {
			if !HasEncryptionEnvelope(se.Metadata) {
				continue // plaintext: a revocation does not touch it
			}
			keyID := GetEncryptionKeyID(se.Metadata)
			if _, want := candidates[keyID]; !want {
				continue
			}
			if !coveredByShred(se, policies, now) {
				outOfScope[keyID]++
			}
		}
		position = batch[len(batch)-1].GlobalPosition
		if len(batch) < m.batchSize {
			break
		}
	}
	return outOfScope, nil
}

// coveredByShred reports whether at least one ActionShred policy matches se on this sweep
// (static matchers AND age), i.e. whether the sweep itself would shred it. Only Shred
// policies count: a RedactFields/Anonymize match does not consent to the event's erasure.
func coveredByShred(se StoredEvent, policies []RetentionPolicy, now time.Time) bool {
	for i := range policies {
		p := policies[i]
		if p.Action == ActionShred && p.matchesStatic(se) && p.ageEligible(se, now) {
			return true
		}
	}
	return false
}

// revoke crypto-shreds every key in report.KeysToRevoke (the guard-approved set).
func (m *RetentionManager) revoke(report *RetentionReport) {
	if len(report.KeysToRevoke) == 0 {
		return
	}
	cfg := m.store.EncryptionConfig()
	if cfg == nil {
		report.Errors = append(report.Errors, ErrErasureNotConfigured)
		return
	}
	for _, k := range report.KeysToRevoke {
		if err := cfg.RevokeKey(k); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("revoke key %q: %w", k, err))
			continue
		}
		report.KeysRevoked = append(report.KeysRevoked, k)
	}
	sort.Strings(report.KeysRevoked)
}
