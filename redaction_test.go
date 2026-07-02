package mink

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeReadModel implements SubjectRedactable.
type fakeReadModel struct {
	name     string
	redacted []string
	err      error
}

func (f *fakeReadModel) RedactSubject(_ context.Context, subjectID string) error {
	if f.err != nil {
		return f.err
	}
	f.redacted = append(f.redacted, subjectID)
	return nil
}

func (f *fakeReadModel) ReadModelName() string { return f.name }

func TestDataEraser_RedactsReadModelHook(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))

	rm := &fakeReadModel{name: "users"}
	res, err := NewDataEraser(store, WithReadModelRedactor(rm)).Erase(ctx, ErasureRequest{
		SubjectID: "u1", Streams: []string{"User-u1"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"users"}, res.RedactedReadModels)
	assert.Empty(t, res.ResidualReadModels)
	assert.Equal(t, []string{"u1"}, rm.redacted)
}

func TestDataEraser_RebuilderTriggered(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))

	rebuilt := false
	res, err := NewDataEraser(store, WithReadModelRebuilder(ReadModelRebuilder{
		Name:    "users-proj",
		Rebuild: func(context.Context) error { rebuilt = true; return nil },
	})).Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-u1"}})
	require.NoError(t, err)
	assert.True(t, rebuilt)
	assert.Equal(t, []string{"users-proj"}, res.RedactedReadModels)
}

func TestDataEraser_RedactionFailureIsResidual(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "a@b.c"}}))

	rm := &fakeReadModel{name: "users", err: errors.New("db down")}
	res, err := NewDataEraser(store, WithReadModelRedactor(rm)).Erase(ctx, ErasureRequest{
		SubjectID: "u1", Streams: []string{"User-u1"},
	})
	require.NoError(t, err) // read-model failure is non-fatal
	assert.Equal(t, []string{"users"}, res.ResidualReadModels)
	assert.NotEmpty(t, res.Errors)
	assert.Empty(t, res.RedactedReadModels)
}

// TestRebuildOverRevokedYieldsRedacted covers 4.1: a load (≈ projection rebuild)
// over crypto-shredded events yields redacted payloads, not a failure.
func TestRebuildOverRevokedYieldsRedacted(t *testing.T) {
	ctx := context.Background()
	store, _ := newEraseTestStore(t, "k")
	require.NoError(t, store.Append(ctx, "User-u1",
		[]interface{}{eraseUserCreated{UserID: "u1", Email: "secret@example.com"}}))

	_, err := NewDataEraser(store).Erase(ctx, ErasureRequest{SubjectID: "u1", Streams: []string{"User-u1"}})
	require.NoError(t, err)

	events, err := store.Load(ctx, "User-u1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	raw, _ := json.Marshal(events[0].Data)
	assert.NotContains(t, string(raw), "secret@example.com")
}
