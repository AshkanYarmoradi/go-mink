package mink

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRetentionSharedKeyError_Error_OmitsKeyID pins the message contract: a
// per-subject key resolver derives key ids from data-subject ids ("key-<subjectID>"),
// so the message — which reaches logs, CLI output and report renderings — carries
// the out-of-scope COUNT only, never the key id. KeyID stays on the struct for
// programmatic use (mirrors SharedKeyError, which prints counts only).
func TestRetentionSharedKeyError_Error_OmitsKeyID(t *testing.T) {
	tests := []struct {
		name string
		err  *RetentionSharedKeyError
		want string
	}{
		{
			name: "per-subject key id is a subject id",
			err:  &RetentionSharedKeyError{KeyID: "key-subject-7f3a9c", OutOfScope: 3},
			want: "mink: retention refused to revoke an encryption key: it also protects 3 event(s) outside the Shred policy scope (blast-radius guard); use a per-scope key or WithAllowSharedKeyRevocation",
		},
		{
			name: "tenant key id",
			err:  &RetentionSharedKeyError{KeyID: "tenant-acme/dek", OutOfScope: 1},
			want: "mink: retention refused to revoke an encryption key: it also protects 1 event(s) outside the Shred policy scope (blast-radius guard); use a per-scope key or WithAllowSharedKeyRevocation",
		},
		{
			name: "zero count still a complete sentence",
			err:  &RetentionSharedKeyError{KeyID: "shared-0", OutOfScope: 0},
			want: "mink: retention refused to revoke an encryption key: it also protects 0 event(s) outside the Shred policy scope (blast-radius guard); use a per-scope key or WithAllowSharedKeyRevocation",
		},
		{
			name: "hostile key id cannot inject into the message",
			err:  &RetentionSharedKeyError{KeyID: "k\nErrors: 0\x1b[2K", OutOfScope: 2},
			want: "mink: retention refused to revoke an encryption key: it also protects 2 event(s) outside the Shred policy scope (blast-radius guard); use a per-scope key or WithAllowSharedKeyRevocation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			assert.Equal(t, tt.want, msg)
			assert.True(t, strings.HasPrefix(msg, "mink: "))
			assert.NotContains(t, msg, tt.err.KeyID, "the key id must never be rendered")
			assert.NotContains(t, msg, `"`, "no quoted identifier of any kind")

			// errors.Is / Unwrap contract and %w wrapping are unchanged.
			assert.ErrorIs(t, tt.err, ErrRetentionSharedKey)
			assert.Equal(t, ErrRetentionSharedKey, errors.Unwrap(tt.err))
			wrapped := fmt.Errorf("sweep: %w", tt.err)
			assert.ErrorIs(t, wrapped, ErrRetentionSharedKey)
			var ske *RetentionSharedKeyError
			require.ErrorAs(t, wrapped, &ske)
			assert.Equal(t, tt.err.KeyID, ske.KeyID, "KeyID stays available programmatically")
			assert.Equal(t, tt.err.OutOfScope, ske.OutOfScope)
			assert.NotContains(t, wrapped.Error(), tt.err.KeyID)
		})
	}
}
