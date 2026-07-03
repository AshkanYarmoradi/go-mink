package mink

import (
	"context"
	"sort"
)

// UnregisteredStreamTypes scans a single stream and returns the distinct event type names
// present in storage that are not registered with the serializer — a read-only pre-deploy /
// migration safety check that never mutates the append-only log. It is empty when every type
// in the stream is registered, and nil when the serializer does not support introspection.
func (s *EventStore) UnregisteredStreamTypes(ctx context.Context, streamID string) ([]string, error) {
	r, ok := s.serializer.(EventTypeRegistrar)
	if !ok {
		return nil, nil
	}
	stored, err := s.adapter.Load(ctx, streamID, 0)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(stored))
	for _, e := range stored {
		seen[e.Type] = struct{}{}
	}
	return unregisteredSorted(seen, r), nil
}

// UnregisteredEventTypes scans the entire event log (in batches) and returns the distinct
// event type names present in storage that are not registered — the all-streams counterpart
// to UnregisteredStreamTypes. Read-only; requires a SubscriptionAdapter for the global scan
// (returns ErrSubscriptionNotSupported otherwise), and nil when the serializer lacks
// introspection.
//
// Completeness caveat: the scan uses the same position-load path as delivery, which on the
// PostgreSQL adapter is capped at the gapless safe watermark. Events from still-in-flight
// transactions are therefore excluded, so run this against a quiescent store for a complete
// pre-deploy/migration audit — under concurrent writers it may miss the most recent events.
func (s *EventStore) UnregisteredEventTypes(ctx context.Context) ([]string, error) {
	r, ok := s.serializer.(EventTypeRegistrar)
	if !ok {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var from uint64
	for {
		batch, err := s.LoadEventsFromPosition(ctx, from, 1000)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, e := range batch {
			seen[e.Type] = struct{}{}
			from = e.GlobalPosition
		}
	}
	return unregisteredSorted(seen, r), nil
}

// unregisteredSorted returns the sorted subset of the seen event type names that r reports
// as not registered.
func unregisteredSorted(seen map[string]struct{}, r EventTypeRegistrar) []string {
	var out []string
	for t := range seen {
		if !r.IsRegistered(t) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}
