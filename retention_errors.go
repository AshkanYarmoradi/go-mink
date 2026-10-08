package mink

import (
	"errors"
	"fmt"
)

// Retention-related sentinel errors.
var (
	// ErrRetentionMaxScanNeedsCheckpoint indicates WithRetentionMaxScan was configured
	// without WithRetentionCheckpoint. A per-run scan cap only makes sense with a
	// checkpoint to resume the remainder on the next run; without one it would re-scan the
	// same oldest events every run and never reach the aged tail. RetentionManager reports
	// this (non-fatally) in RetentionReport.Errors and runs the sweep unbounded rather than
	// silently capping and forgetting.
	ErrRetentionMaxScanNeedsCheckpoint = errors.New("mink: WithRetentionMaxScan requires WithRetentionCheckpoint to resume across runs; scanning unbounded")

	// ErrRetentionUnscopedPolicy indicates a RetentionPolicy with no matcher at all
	// (Category, StreamPrefix, EventTypes and TenantID empty and MaxAge zero). Such a
	// policy matches EVERY event in the store — for ActionShred that is a whole-store
	// crypto-shred. RetentionPolicy.Validate rejects it for every action, and
	// RetentionManager reports it in RetentionReport.Errors and leaves the policy inert
	// (it never matches, acts or revokes) rather than sweeping the entire log.
	ErrRetentionUnscopedPolicy = errors.New("mink: retention policy has no matchers and would match every event; set at least one of Category, StreamPrefix, EventTypes, TenantID or MaxAge")

	// ErrRetentionSharedKey indicates the shared-key blast-radius guard refused to revoke
	// an encryption key because the key also protects events that no Shred policy in this
	// sweep covers (outside the policies' Category/StreamPrefix/EventTypes/TenantID scope,
	// or not yet older than MaxAge). Revoking it would crypto-shred those events too. The
	// key is listed in RetentionReport.SharedKeysSkipped and the sweep is reported as
	// incomplete (Failed() is true). Give each retention scope its own key (per-subject /
	// per-tenant keys via WithSubjectKeyResolver), or — accepting the blast radius —
	// disable the guard with WithAllowSharedKeyRevocation.
	ErrRetentionSharedKey = errors.New("mink: retention refused to revoke an encryption key shared with events outside the policy scope (blast-radius guard)")

	// ErrRetentionUnencryptedMatches indicates an ActionShred policy matched events that
	// carry no field-encryption envelope. Crypto-shredding can only erase ciphertext, so
	// those events remain in plaintext after the sweep; RetentionManager reports the count
	// in RetentionReport.UnencryptedMatches and adds this error so a shred sweep never
	// looks fully successful while matched plaintext remains.
	ErrRetentionUnencryptedMatches = errors.New("mink: retention shred matched events with no field-encryption envelope; they remain in plaintext")
)

// RetentionSharedKeyError reports one encryption key the shared-key blast-radius guard
// refused to revoke during a retention sweep. OutOfScope is the number of events
// encrypted under KeyID that no Shred policy in the sweep covers (PII-free: a count, never
// the events). It matches ErrRetentionSharedKey via errors.Is.
type RetentionSharedKeyError struct {
	KeyID      string
	OutOfScope int
}

// Error returns the error message.
func (e *RetentionSharedKeyError) Error() string {
	return fmt.Sprintf("mink: retention refused to revoke key %q: it also protects %d event(s) outside the Shred policy scope (blast-radius guard); use a per-scope key or WithAllowSharedKeyRevocation",
		e.KeyID, e.OutOfScope)
}

// Is reports whether this error matches the target error.
func (e *RetentionSharedKeyError) Is(target error) bool {
	return target == ErrRetentionSharedKey
}

// Unwrap returns the sentinel this error wraps.
func (e *RetentionSharedKeyError) Unwrap() error {
	return ErrRetentionSharedKey
}
