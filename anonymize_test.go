package mink

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnonymizer_Deterministic(t *testing.T) {
	a := NewAnonymizer([]byte("secret"))
	p1 := a.Pseudonymize("email", "alice@example.com")
	p2 := a.Pseudonymize("email", "alice@example.com")
	assert.Equal(t, p1, p2, "equal inputs must yield equal pseudonyms")
	assert.NotEqual(t, "alice@example.com", p1)
	assert.NotContains(t, p1, "alice", "pseudonym must not reveal the original")
}

func TestAnonymizer_ScopeSeparation(t *testing.T) {
	a := NewAnonymizer([]byte("secret"))
	assert.NotEqual(t, a.Pseudonymize("email", "x"), a.Pseudonymize("name", "x"))
}

func TestAnonymizer_PrefixAndLength(t *testing.T) {
	a := NewAnonymizer([]byte("s"), WithPseudonymPrefix("anon_"), WithPseudonymLength(8))
	p := a.Pseudonymize("f", "v")
	assert.True(t, strings.HasPrefix(p, "anon_"))
	assert.Len(t, p, len("anon_")+8)
}

// 8.2: Anonymize is selectable in a retention policy via its Apply hook.
func TestAnonymizer_InRetentionPolicy(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))

	a := NewAnonymizer([]byte("s"))
	var pseudonyms []string
	mgr := NewRetentionManager(store, []RetentionPolicy{{
		Name: "anon", StreamPrefix: "User-", Action: ActionAnonymize, Fields: []string{"userId"},
		Apply: func(_ context.Context, se StoredEvent) error {
			pseudonyms = append(pseudonyms, a.Pseudonymize("userId", se.StreamID))
			return nil
		},
	}})
	report, err := mgr.Apply(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Acted)
	require.Len(t, pseudonyms, 1)
	assert.NotContains(t, pseudonyms[0], "User-u1")
}

// An empty secret degrades HMAC to an unkeyed hash; the constructor keeps its signature
// but Validate must reject it so callers can fail fast before anonymizing with it.
func TestAnonymizer_Validate(t *testing.T) {
	tests := []struct {
		name    string
		a       *Anonymizer
		wantErr error
	}{
		{"nil receiver", nil, ErrAnonymizerSecretRequired},
		{"empty secret", NewAnonymizer(nil), ErrAnonymizerSecretRequired},
		{"zero-length secret", NewAnonymizer([]byte{}), ErrAnonymizerSecretRequired},
		{"keyed", NewAnonymizer([]byte("secret")), nil},
		{"keyed with options", NewAnonymizer([]byte("s"), WithPseudonymPrefix("anon_"), WithPseudonymLength(8)), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.a.Validate()
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
			assert.True(t, strings.HasPrefix(err.Error(), "mink: "), "sentinel is mink-prefixed: %q", err.Error())
		})
	}
}

// Documents the hazard Validate guards against: with an empty secret the pseudonym is a
// plain unkeyed hash, so anyone can recompute it for a guessed value.
func TestAnonymizer_EmptySecretIsUnkeyed(t *testing.T) {
	unkeyed := NewAnonymizer(nil)
	again := NewAnonymizer([]byte{})
	assert.Equal(t, unkeyed.Pseudonymize("email", "alice@example.com"), again.Pseudonymize("email", "alice@example.com"),
		"no secret ⇒ any party computes the same pseudonym")
	keyed := NewAnonymizer([]byte("secret"))
	assert.NotEqual(t, unkeyed.Pseudonymize("email", "alice@example.com"), keyed.Pseudonymize("email", "alice@example.com"))
}
