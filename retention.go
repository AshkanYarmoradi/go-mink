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

func (p RetentionPolicy) matches(se StoredEvent, now time.Time) bool {
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
	if p.MaxAge > 0 && now.Sub(se.Timestamp) < p.MaxAge {
		return false
	}
	return true
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
	DryRun      bool
	Scanned     int      // events examined
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
	store     *EventStore
	policies  []RetentionPolicy
	batchSize int
	now       func() time.Time
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
	now := m.now()
	shredKeys := map[string]struct{}{}

	var position uint64
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
			for i := range m.policies {
				p := m.policies[i]
				if !p.matches(se, now) {
					continue
				}
				report.Matched++
				if dryRun {
					continue
				}
				m.act(ctx, p, se, shredKeys, report)
			}
		}
		position = batch[len(batch)-1].GlobalPosition
	}

	if !dryRun && len(shredKeys) > 0 {
		m.revoke(shredKeys, report)
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
