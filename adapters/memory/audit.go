package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"go-mink.dev/adapters"
)

// Ensure interface compliance at compile time
var _ adapters.AuditStore = (*AuditStore)(nil)

// AuditStore provides an in-memory implementation of adapters.AuditStore.
// It is useful for testing and development but should not be used in production
// as it does not persist data across restarts.
type AuditStore struct {
	mu      sync.RWMutex
	entries []*adapters.AuditEntry
}

// NewAuditStore creates a new in-memory AuditStore.
func NewAuditStore() *AuditStore {
	return &AuditStore{
		entries: make([]*adapters.AuditEntry, 0),
	}
}

// Initialize is a no-op for the in-memory store.
func (s *AuditStore) Initialize(ctx context.Context) error {
	return nil
}

// Close is a no-op for the in-memory store.
func (s *AuditStore) Close() error {
	return nil
}

// Append stores a copy of the audit entry.
func (s *AuditStore) Append(ctx context.Context, entry *adapters.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = append(s.entries, adapters.CopyAuditEntry(entry))
	return nil
}

// matches reports whether an entry satisfies every predicate in the query.
func matchesAuditQuery(entry *adapters.AuditEntry, q adapters.AuditQuery) bool {
	if q.CommandType != "" && entry.CommandType != q.CommandType {
		return false
	}
	if q.Actor != "" && entry.Actor != q.Actor {
		return false
	}
	if q.TenantID != "" && entry.TenantID != q.TenantID {
		return false
	}
	if q.AggregateID != "" && entry.AggregateID != q.AggregateID {
		return false
	}
	// From is inclusive: keep entries with Timestamp >= From.
	if !q.From.IsZero() && entry.Timestamp.Before(q.From) {
		return false
	}
	// To is exclusive: keep entries with Timestamp < To.
	if !q.To.IsZero() && !entry.Timestamp.Before(q.To) {
		return false
	}
	if q.Success != nil && entry.Success != *q.Success {
		return false
	}
	return true
}

// filter returns copies of all entries matching the query, without applying
// ordering or pagination.
func (s *AuditStore) filter(q adapters.AuditQuery) []*adapters.AuditEntry {
	matched := make([]*adapters.AuditEntry, 0)
	for _, entry := range s.entries {
		if matchesAuditQuery(entry, q) {
			matched = append(matched, adapters.CopyAuditEntry(entry))
		}
	}
	return matched
}

// Find returns audit entries matching the query, honoring Order/Limit/Offset.
func (s *AuditStore) Find(ctx context.Context, q adapters.AuditQuery) ([]*adapters.AuditEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	matched := s.filter(q)

	// Sort by timestamp per Order, tie-breaking on ID so pagination is
	// deterministic even when entries share a timestamp (mirrors the SQL store).
	sort.SliceStable(matched, func(i, j int) bool {
		ti, tj := matched[i].Timestamp, matched[j].Timestamp
		if !ti.Equal(tj) {
			if q.Order == adapters.AuditOrderTimestampAsc {
				return ti.Before(tj)
			}
			return ti.After(tj)
		}
		if q.Order == adapters.AuditOrderTimestampAsc {
			return matched[i].ID < matched[j].ID
		}
		return matched[i].ID > matched[j].ID
	})

	// Apply Offset, then Limit.
	if q.Offset > 0 {
		if q.Offset >= len(matched) {
			return []*adapters.AuditEntry{}, nil
		}
		matched = matched[q.Offset:]
	}
	if q.Limit > 0 && q.Limit < len(matched) {
		matched = matched[:q.Limit]
	}

	return matched, nil
}

// Count returns the number of entries matching the query.
// It ignores Limit and Offset.
func (s *AuditStore) Count(ctx context.Context, q adapters.AuditQuery) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int64
	for _, entry := range s.entries {
		if matchesAuditQuery(entry, q) {
			count++
		}
	}
	return count, nil
}

// Cleanup removes entries with a Timestamp older than olderThan ago.
// Returns the number of entries removed.
func (s *AuditStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	kept := make([]*adapters.AuditEntry, 0, len(s.entries))
	var removed int64
	for _, entry := range s.entries {
		if entry.Timestamp.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	s.entries = kept
	return removed, nil
}

// Len returns the number of entries in the store.
// Useful for testing.
func (s *AuditStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Clear removes all entries from the store.
// Useful for testing.
func (s *AuditStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make([]*adapters.AuditEntry, 0)
}
