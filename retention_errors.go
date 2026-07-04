package mink

import "errors"

// Retention-related sentinel errors.
var (
	// ErrRetentionMaxScanNeedsCheckpoint indicates WithRetentionMaxScan was configured
	// without WithRetentionCheckpoint. A per-run scan cap only makes sense with a
	// checkpoint to resume the remainder on the next run; without one it would re-scan the
	// same oldest events every run and never reach the aged tail. RetentionManager reports
	// this (non-fatally) in RetentionReport.Errors and runs the sweep unbounded rather than
	// silently capping and forgetting.
	ErrRetentionMaxScanNeedsCheckpoint = errors.New("mink: WithRetentionMaxScan requires WithRetentionCheckpoint to resume across runs; scanning unbounded")
)
