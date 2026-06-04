package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters"
)

// TestPostgresIdempotencyStore_StoreIfAbsent_ConcurrentRace verifies that under a
// real concurrent race on the same key, exactly one StoreIfAbsent wins. The DB
// unique constraint — not application timing — is what enforces single execution,
// which is the whole point of the reservation primitive.
func TestPostgresIdempotencyStore_StoreIfAbsent_ConcurrentRace(t *testing.T) {
	store := setupIdempotencyTestStore(t)
	ctx := context.Background()
	key := fmt.Sprintf("race-%d", time.Now().UnixNano())

	const racers = 8
	oks := make([]bool, racers)
	errs := make([]error, racers)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := &adapters.IdempotencyRecord{
				Key:         key,
				CommandType: "RaceCommand",
				Success:     true,
				ProcessedAt: time.Now(),
				ExpiresAt:   time.Now().Add(time.Hour),
			}
			<-start // release all racers together to maximize contention
			oks[i], errs[i] = store.StoreIfAbsent(ctx, rec)
		}(i)
	}
	close(start)
	wg.Wait()

	wins := 0
	for i := range errs {
		require.NoError(t, errs[i])
		if oks[i] {
			wins++
		}
	}
	assert.Equal(t, 1, wins, "exactly one racer may reserve the key")
}
