package adapters

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFeedFilter_IsEmpty(t *testing.T) {
	assert.True(t, FeedFilter{}.IsEmpty())
	assert.False(t, FeedFilter{EventTypes: []string{"X"}}.IsEmpty())
	assert.False(t, FeedFilter{StreamIDs: []string{"s-1"}}.IsEmpty())
	assert.False(t, FeedFilter{Category: "order"}.IsEmpty())
}

func TestFeedFilter_Matches(t *testing.T) {
	ev := StoredEvent{Type: "OrderPlaced", StreamID: "order-123"}

	// Empty filter matches everything.
	assert.True(t, FeedFilter{}.Matches(ev))

	// Event type axis — OR within the set.
	assert.True(t, FeedFilter{EventTypes: []string{"OrderPlaced"}}.Matches(ev))
	assert.True(t, FeedFilter{EventTypes: []string{"X", "OrderPlaced"}}.Matches(ev))
	assert.False(t, FeedFilter{EventTypes: []string{"OrderShipped"}}.Matches(ev))

	// Stream id axis.
	assert.True(t, FeedFilter{StreamIDs: []string{"order-123"}}.Matches(ev))
	assert.False(t, FeedFilter{StreamIDs: []string{"order-999"}}.Matches(ev))

	// Category axis is a "<cat>-" prefix, not a bare substring.
	assert.True(t, FeedFilter{Category: "order"}.Matches(ev))
	assert.False(t, FeedFilter{Category: "invoice"}.Matches(ev))
	assert.False(t, FeedFilter{Category: "order-1"}.Matches(ev), "order-1- is not a prefix of order-123")
	assert.False(t, FeedFilter{Category: "ord"}.Matches(StoredEvent{StreamID: "order-1"}), "ord is not the category order")

	// Axes AND-compose.
	assert.True(t, FeedFilter{EventTypes: []string{"OrderPlaced"}, Category: "order"}.Matches(ev))
	assert.False(t, FeedFilter{EventTypes: []string{"OrderPlaced"}, Category: "invoice"}.Matches(ev))
}
