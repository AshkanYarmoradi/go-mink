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
