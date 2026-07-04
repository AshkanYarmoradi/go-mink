package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev"
	"go-mink.dev/adapters"
)

// TestPostgresAdapter_LoadFromPosition_SafeWatermark proves the gapless safe-watermark
// on the shared read path (LoadFromPosition — used by SubscribeAll, async projections,
// and sagas): a committed higher global_position is held back while a lower position is
// still in an in-flight transaction, so it is never delivered/checkpointed past and then
// skipped when the lower position commits; and a rolled-back position is a permanent gap
// that must not stall delivery.
func TestPostgresAdapter_LoadFromPosition_SafeWatermark(t *testing.T) {
	db := getTestDB(t)
	schema := newTestSchema()
	t.Cleanup(func() { cleanupSchema(t, db, schema); _ = db.Close() })
	adapter := newTestAdapter(t, db, WithSchema(schema))
	require.NoError(t, adapter.Initialize(context.Background()))
	ctx := context.Background()

	// Raw insert into <schema>.events at a high version (no collision with adapter appends,
	// which use sequential low versions; the "wm" streams row exists so any FK is satisfied).
	rawInsert := fmt.Sprintf(`INSERT INTO "%s".events (stream_id, version, event_type, data, metadata) VALUES ($1,$2,$3,$4,$5)`, schema)

	base, err := adapter.Append(ctx, "wm", []adapters.EventRecord{{Type: "E", Data: []byte(`{}`)}}, mink.NoStream)
	require.NoError(t, err)
	cursor := base[0].GlobalPosition

	t.Run("in-flight lower position holds back a committed higher position", func(t *testing.T) {
		// Raw INSERT of a lower global_position, left in-flight (uncommitted).
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, rawInsert, "wm", 1000, "E", []byte(`{}`), []byte(`{}`))
		require.NoError(t, err)

		// A higher global_position committed via a normal append on another stream.
		_, err = adapter.Append(ctx, "hi1", []adapters.EventRecord{{Type: "E", Data: []byte(`{}`)}}, mink.NoStream)
		require.NoError(t, err)

		// The committed higher position must be held back while the lower one is in-flight.
		got, err := adapter.LoadFromPosition(ctx, cursor, 100)
		require.NoError(t, err)
		assert.Empty(t, got, "committed higher position must be held back behind an in-flight lower position")

		// Commit the in-flight lower position; now both are delivered, in position order.
		require.NoError(t, tx.Commit())
		got, err = adapter.LoadFromPosition(ctx, cursor, 100)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Less(t, got[0].GlobalPosition, got[1].GlobalPosition, "delivered in position order, none skipped")
	})

	t.Run("a rolled-back position is a permanent gap that does not stall", func(t *testing.T) {
		b, err := adapter.Append(ctx, "wm", []adapters.EventRecord{{Type: "E", Data: []byte(`{}`)}}, mink.AnyVersion)
		require.NoError(t, err)
		before := b[0].GlobalPosition

		// Raw INSERT that ROLLS BACK — consumes a global_position but leaves a permanent gap.
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, rawInsert, "wm", 1001, "E", []byte(`{}`), []byte(`{}`))
		require.NoError(t, err)
		require.NoError(t, tx.Rollback())

		// A later committed event must still be delivered (not stalled by the permanent gap).
		_, err = adapter.Append(ctx, "hi2", []adapters.EventRecord{{Type: "E", Data: []byte(`{}`)}}, mink.NoStream)
		require.NoError(t, err)

		got, err := adapter.LoadFromPosition(ctx, before, 100)
		require.NoError(t, err)
		require.NotEmpty(t, got, "delivery must not stall on a rolled-back permanent gap")
	})
}
