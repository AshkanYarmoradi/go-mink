package mink

import "context"

// ErasureContext is passed to erasure side-effect hooks. It carries only references
// (subject id, affected streams, revoked keys) — never the erased PII itself.
type ErasureContext struct {
	SubjectID   string
	Streams     []string
	KeysRevoked []string
}

// ErasureHook erases PII an application owns OUTSIDE the event store (blob storage,
// generated exports/PDFs, caches, search indexes, short URLs) as part of Erase. Run
// failures are reported on ErasureResult, never fatal. Register with WithErasureHook.
type ErasureHook struct {
	Name string
	Run  func(ctx context.Context, ec ErasureContext) error
}
