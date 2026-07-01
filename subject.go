package mink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go-mink.dev/adapters"
)

// subjectTagsKey is the reserved Metadata.Custom key under which subject tags are
// recorded (a JSON array of subject ids).
const subjectTagsKey = "$subjects"

// SubjectTagger derives the data-subject identifier(s) a freshly-appended event
// concerns, from its serialized data and metadata. The returned ids are recorded
// in Metadata.Custom so the subject's complete footprint can later be resolved for
// GDPR export/erasure. Returning nil tags nothing (zero overhead). Configure via
// WithSubjectTagger.
//
// data is the serialized event payload (the bytes being appended); applied at the
// single shared prepare-event hook, so it covers Append, SaveAggregate, and the
// outbox uniformly. Most taggers derive the subject from md (UserID/TenantID) or a
// known field within data.
type SubjectTagger func(eventType string, data []byte, md Metadata) []string

// setSubjectTags records subjects in Metadata.Custom (JSON array), merging with
// any already present, de-duplicated and order-preserving.
func setSubjectTags(m Metadata, subjects []string) Metadata {
	set := make(map[string]struct{})
	merged := make([]string, 0, len(subjects))
	for _, s := range append(GetSubjectTags(m), subjects...) {
		if s == "" {
			continue
		}
		if _, ok := set[s]; ok {
			continue
		}
		set[s] = struct{}{}
		merged = append(merged, s)
	}
	if len(merged) == 0 {
		return m
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return m
	}
	return m.WithCustom(subjectTagsKey, string(b))
}

// GetSubjectTags returns the data-subject ids recorded on an event's metadata, or
// nil if none.
func GetSubjectTags(m Metadata) []string {
	if m.Custom == nil {
		return nil
	}
	v, ok := m.Custom[subjectTagsKey]
	if !ok {
		return nil
	}
	var subjects []string
	if err := json.Unmarshal([]byte(v), &subjects); err != nil {
		return nil
	}
	return subjects
}

// eventTagsSubject reports whether an event's metadata tags the given subject.
func eventTagsSubject(m Metadata, subjectID string) bool {
	for _, s := range GetSubjectTags(m) {
		if s == subjectID {
			return true
		}
	}
	return false
}

// SubjectFilter returns an ExportFilter matching events tagged with subjectID. It
// bridges subject tagging into the export/erasure scan model.
func SubjectFilter(subjectID string) ExportFilter {
	return func(e StoredEvent) bool {
		return eventTagsSubject(e.Metadata, subjectID)
	}
}

// SubjectFootprint describes the complete extent of a data subject's events. It
// drives complete-by-default export and erasure and doubles as an erasure preview.
type SubjectFootprint struct {
	SubjectID         string
	Streams           []string       // sorted, de-duplicated
	StreamEventCounts map[string]int // tagged events per stream
	EventCount        int            // total tagged events
	KeyIDs            []string       // distinct encryption key ids on tagged events (sorted)

	// Partial is true when completeness cannot be proven — e.g. the store contains
	// untagged (legacy) events that could belong to the subject. Callers MUST treat
	// a partial footprint as incomplete (never a silent partial).
	Partial bool
}

// SubjectIndexAdapter is an OPTIONAL adapter extension that resolves a subject's
// streams from an index, avoiding a full scan. Adapters MAY implement it; the
// resolver falls back to a scan otherwise.
type SubjectIndexAdapter interface {
	StreamsBySubject(ctx context.Context, subjectID string) ([]string, error)
}

// SubjectResolver resolves a subject id to its complete footprint across all
// streams, using the adapter's subject index when available or a scan otherwise.
type SubjectResolver struct {
	store     *EventStore
	batchSize int
}

// SubjectResolverOption configures a SubjectResolver.
type SubjectResolverOption func(*SubjectResolver)

// WithResolverBatchSize sets the scan batch size (default 1000).
func WithResolverBatchSize(size int) SubjectResolverOption {
	return func(r *SubjectResolver) {
		if size > 0 {
			r.batchSize = size
		}
	}
}

// NewSubjectResolver creates a resolver for the given store.
func NewSubjectResolver(store *EventStore, opts ...SubjectResolverOption) *SubjectResolver {
	r := &SubjectResolver{store: store, batchSize: 1000}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Resolve returns the subject's footprint. It is read-only and therefore doubles
// as an erasure preview.
func (r *SubjectResolver) Resolve(ctx context.Context, subjectID string) (*SubjectFootprint, error) {
	if subjectID == "" {
		return nil, ErrSubjectIDRequired
	}

	fp := &SubjectFootprint{SubjectID: subjectID, StreamEventCounts: map[string]int{}}
	streamSet := map[string]struct{}{}
	keySet := map[string]struct{}{}

	// Index-backed fast path.
	if idx, ok := r.store.Adapter().(SubjectIndexAdapter); ok {
		streams, err := idx.StreamsBySubject(ctx, subjectID)
		if err != nil {
			return nil, fmt.Errorf("mink: subject index for %q: %w", subjectID, err)
		}
		for _, streamID := range streams {
			stored, err := r.store.LoadRaw(ctx, streamID, 0)
			if err != nil {
				if errors.Is(err, ErrStreamNotFound) {
					continue
				}
				return nil, fmt.Errorf("mink: load stream %q for subject %q: %w", streamID, subjectID, err)
			}
			for _, se := range stored {
				if eventTagsSubject(se.Metadata, subjectID) {
					r.record(fp, streamSet, keySet, se)
				}
			}
		}
		r.finalize(fp, streamSet, keySet)
		return fp, nil
	}

	// Scan fallback.
	if _, ok := r.store.Adapter().(adapters.SubscriptionAdapter); !ok {
		return nil, ErrExportScanNotSupported
	}
	var position uint64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := r.store.LoadEventsFromPosition(ctx, position, r.batchSize)
		if err != nil {
			return nil, fmt.Errorf("mink: subject scan from %d: %w", position, err)
		}
		if len(batch) == 0 {
			break
		}
		for _, se := range batch {
			switch {
			case eventTagsSubject(se.Metadata, subjectID):
				r.record(fp, streamSet, keySet, se)
			case len(GetSubjectTags(se.Metadata)) == 0:
				// An untagged event: tagging was not universally applied, so the
				// footprint cannot be proven complete.
				fp.Partial = true
			}
		}
		position = batch[len(batch)-1].GlobalPosition
	}
	r.finalize(fp, streamSet, keySet)
	return fp, nil
}

func (r *SubjectResolver) record(fp *SubjectFootprint, streamSet, keySet map[string]struct{}, se StoredEvent) {
	fp.EventCount++
	fp.StreamEventCounts[se.StreamID]++
	streamSet[se.StreamID] = struct{}{}
	if k := GetEncryptionKeyID(se.Metadata); k != "" {
		keySet[k] = struct{}{}
	}
}

func (r *SubjectResolver) finalize(fp *SubjectFootprint, streamSet, keySet map[string]struct{}) {
	fp.Streams = sortedSet(streamSet)
	fp.KeyIDs = sortedSet(keySet)
}
