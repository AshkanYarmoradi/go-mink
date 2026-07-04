package memory

import (
	"context"
	"fmt"

	"go-mink.dev/adapters"
)

var _ adapters.EventRewriteAdapter = (*MemoryAdapter)(nil)

// RewriteEventData replaces the data and metadata of the event at (streamID,
// version) in both the stream log and the global log (events are stored by value in
// each), preserving all other fields. See adapters.EventRewriteAdapter. Errors if no
// such event exists.
func (a *MemoryAdapter) RewriteEventData(ctx context.Context, streamID string, version int64, data []byte, metadata adapters.Metadata) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return adapters.ErrAdapterClosed
	}
	stream, ok := a.streams[streamID]
	if !ok {
		return fmt.Errorf("mink/memory: no event at stream %q version %d to rewrite", streamID, version)
	}
	found := false
	for i := range stream.events {
		if stream.events[i].Version == version {
			stream.events[i].Data = data
			stream.events[i].Metadata = metadata
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("mink/memory: no event at stream %q version %d to rewrite", streamID, version)
	}
	// Keep the global log consistent with the per-stream log.
	for i := range a.globalEvents {
		if a.globalEvents[i].StreamID == streamID && a.globalEvents[i].Version == version {
			a.globalEvents[i].Data = data
			a.globalEvents[i].Metadata = metadata
			break
		}
	}
	return nil
}
