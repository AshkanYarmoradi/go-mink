package mink

import "context"

// SubjectRedactable is implemented by a read model / projection that can redact a
// single data subject's PII in place — far cheaper than a full rebuild. DataEraser
// prefers it when available; register one with WithReadModelRedactor.
type SubjectRedactable interface {
	// RedactSubject removes or masks the subject's personal data from the read model.
	RedactSubject(ctx context.Context, subjectID string) error
	// ReadModelName identifies the read model for reporting on ErasureResult.
	ReadModelName() string
}

// ReadModelRebuilder pairs a projection name with the function that rebuilds it
// over the (now crypto-shredded) event stream. DataEraser triggers it when no
// in-place SubjectRedactable hook exists, so revoked-key events re-materialize as
// redacted (the existing WithDecryptionErrorHandler yields redacted payloads on
// ErrKeyRevoked). Register one with WithReadModelRebuilder.
type ReadModelRebuilder struct {
	Name    string
	Rebuild func(ctx context.Context) error
}
