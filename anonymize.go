package mink

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// Anonymizer produces stable pseudonyms for PII values. It is deterministic (equal
// inputs yield equal pseudonyms, preserving referential/analytic continuity) and
// one-way (the original value cannot be recovered from the pseudonym). It is the
// transform behind ActionAnonymize, usable from retention Apply hooks and the
// eraser's read-model redactors.
type Anonymizer struct {
	secret []byte
	prefix string
	length int
}

// AnonymizerOption configures an Anonymizer.
type AnonymizerOption func(*Anonymizer)

// WithPseudonymPrefix prepends a fixed prefix to every pseudonym (e.g. "anon_").
func WithPseudonymPrefix(prefix string) AnonymizerOption {
	return func(a *Anonymizer) { a.prefix = prefix }
}

// WithPseudonymLength caps the hex length of the pseudonym (default 32). A value
// <= 0 or >= 64 uses the full SHA-256 hex.
func WithPseudonymLength(n int) AnonymizerOption {
	return func(a *Anonymizer) { a.length = n }
}

// NewAnonymizer creates an Anonymizer keyed by secret. The secret MUST be kept
// confidential — knowing it does not reveal originals, but it gates linkability.
func NewAnonymizer(secret []byte, opts ...AnonymizerOption) *Anonymizer {
	a := &Anonymizer{secret: append([]byte(nil), secret...), length: 32}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Pseudonymize returns a stable, one-way pseudonym for value within scope (e.g. a
// field name or tenant id, so the same value pseudonymizes differently per scope).
func (a *Anonymizer) Pseudonymize(scope, value string) string {
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte(scope))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	sum := hex.EncodeToString(mac.Sum(nil))
	if a.length > 0 && a.length < len(sum) {
		sum = sum[:a.length]
	}
	return a.prefix + sum
}
