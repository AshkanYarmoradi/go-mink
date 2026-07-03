package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev"
	"go-mink.dev/adapters"
)

// TestPostgresAdapter_loadCategoryEvents_EscapesLikeMetacharacters verifies that a category
// containing a LIKE metacharacter (`_`) matches only its own streams, not LIKE-adjacent
// ones — i.e. `escapeLikePattern` is applied to the category prefix.
func TestPostgresAdapter_loadCategoryEvents_EscapesLikeMetacharacters(t *testing.T) {
	adapter := setupIntegrationTest(t)
	ctx := context.Background()

	_, err := adapter.Append(ctx, "User_-1", []adapters.EventRecord{{Type: "E", Data: []byte(`{}`)}}, mink.NoStream)
	require.NoError(t, err)
	_, err = adapter.Append(ctx, "UserX-1", []adapters.EventRecord{{Type: "E", Data: []byte(`{}`)}}, mink.NoStream)
	require.NoError(t, err)

	// Category "User_": the underscore is a LIKE wildcard; unescaped, `User_-%` would also
	// match "UserX-1". Escaped, only "User_-*" streams match.
	events, err := adapter.loadCategoryEvents(ctx, "User_", 0, 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "User_-1", events[0].StreamID)
}
