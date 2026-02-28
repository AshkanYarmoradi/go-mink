package mink

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
)

const (
	// schemaVersionKey is the metadata key used to store the schema version.
	// The $ prefix marks it as system-managed, preventing collisions with user metadata.
	schemaVersionKey = "$schema_version"

	// DefaultSchemaVersion is the schema version assumed for events without an explicit version.
	// All events stored before versioning was introduced are treated as version 1.
	DefaultSchemaVersion = 1
)

// Upcaster transforms event data from one schema version to the next.
// Upcasters operate on raw bytes and are serializer-agnostic.
// Each upcaster handles exactly one version transition (FromVersion → ToVersion).
type Upcaster interface {
	// EventType returns the event type this upcaster handles.
	EventType() string

	// FromVersion returns the source schema version.
	FromVersion() int

	// ToVersion returns the target schema version. Must equal FromVersion() + 1.
	ToVersion() int

	// Upcast transforms event data from FromVersion to ToVersion.
	// Metadata is provided as read-only context (e.g., for tenant-specific defaults).
	Upcast(data []byte, metadata Metadata) ([]byte, error)
}

// UpcasterChain is a thread-safe registry of upcasters that applies them in sequence.
// It validates that there are no gaps or duplicates in the version chain.
type UpcasterChain struct {
	mu        sync.RWMutex
	upcasters map[string][]Upcaster // eventType → sorted by FromVersion
	latest    map[string]int        // eventType → latest version
}

// NewUpcasterChain creates a new empty UpcasterChain.
func NewUpcasterChain() *UpcasterChain {
	return &UpcasterChain{
		upcasters: make(map[string][]Upcaster),
		latest:    make(map[string]int),
	}
}

// Register adds an upcaster to the chain.
// Returns an error if the upcaster's ToVersion != FromVersion + 1 or if
// an upcaster for the same event type and version transition already exists.
func (c *UpcasterChain) Register(u Upcaster) error {
	if u.ToVersion() != u.FromVersion()+1 {
		return fmt.Errorf("mink: upcaster for %q must have ToVersion (%d) == FromVersion (%d) + 1",
			u.EventType(), u.ToVersion(), u.FromVersion())
	}

	if u.FromVersion() < 1 {
		return fmt.Errorf("mink: upcaster for %q must have FromVersion >= 1, got %d",
			u.EventType(), u.FromVersion())
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	eventType := u.EventType()
	existing := c.upcasters[eventType]

	// Check for duplicates
	for _, e := range existing {
		if e.FromVersion() == u.FromVersion() {
			return fmt.Errorf("mink: duplicate upcaster for %q from version %d to %d",
				eventType, u.FromVersion(), u.ToVersion())
		}
	}

	// Insert and keep sorted by FromVersion
	c.upcasters[eventType] = append(existing, u)
	sort.Slice(c.upcasters[eventType], func(i, j int) bool {
		return c.upcasters[eventType][i].FromVersion() < c.upcasters[eventType][j].FromVersion()
	})

	// Update latest version
	if u.ToVersion() > c.latest[eventType] {
		c.latest[eventType] = u.ToVersion()
	}

	return nil
}

// Validate checks the entire chain for gaps.
// For each event type, the upcasters must form a contiguous chain from the
// lowest FromVersion to the highest ToVersion.
func (c *UpcasterChain) Validate() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for eventType, upcasters := range c.upcasters {
		if len(upcasters) == 0 {
			continue
		}

		// Check for contiguous chain
		for i := 1; i < len(upcasters); i++ {
			expected := upcasters[i-1].ToVersion()
			actual := upcasters[i].FromVersion()
			if actual != expected {
				return NewSchemaVersionGapError(eventType, expected, actual)
			}
		}
	}

	return nil
}

// Upcast transforms event data from fromVersion to the latest version.
// Returns the transformed data, the final version, and any error.
// If no upcasters exist for the event type or the data is already at the latest version,
// the original data is returned unchanged.
func (c *UpcasterChain) Upcast(eventType string, fromVersion int, data []byte, metadata Metadata) ([]byte, int, error) {
	c.mu.RLock()
	upcasters := c.upcasters[eventType]
	c.mu.RUnlock()

	if len(upcasters) == 0 {
		return data, fromVersion, nil
	}

	currentVersion := fromVersion
	currentData := data

	for _, u := range upcasters {
		if u.FromVersion() < currentVersion {
			continue
		}
		if u.FromVersion() > currentVersion {
			// Gap — the event is at a version we can't upcast from
			break
		}

		result, err := u.Upcast(currentData, metadata)
		if err != nil {
			return nil, currentVersion, NewUpcastError(eventType, u.FromVersion(), u.ToVersion(), err)
		}

		currentData = result
		currentVersion = u.ToVersion()
	}

	return currentData, currentVersion, nil
}

// HasUpcasters reports whether any upcasters are registered for the given event type.
func (c *UpcasterChain) HasUpcasters(eventType string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.upcasters[eventType]) > 0
}

// LatestVersion returns the latest schema version for the given event type.
// Returns DefaultSchemaVersion if no upcasters are registered for the type.
func (c *UpcasterChain) LatestVersion(eventType string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if v, ok := c.latest[eventType]; ok {
		return v
	}
	return DefaultSchemaVersion
}

// RegisteredEventTypes returns a sorted list of event types that have upcasters registered.
func (c *UpcasterChain) RegisteredEventTypes() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	types := make([]string, 0, len(c.upcasters))
	for t := range c.upcasters {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// GetSchemaVersion extracts the schema version from event metadata.
// Returns DefaultSchemaVersion if the version is not set or cannot be parsed.
func GetSchemaVersion(m Metadata) int {
	if m.Custom == nil {
		return DefaultSchemaVersion
	}
	v, ok := m.Custom[schemaVersionKey]
	if !ok {
		return DefaultSchemaVersion
	}
	version, err := strconv.Atoi(v)
	if err != nil {
		return DefaultSchemaVersion
	}
	return version
}

// SetSchemaVersion returns a copy of Metadata with the schema version set.
func SetSchemaVersion(m Metadata, version int) Metadata {
	return m.WithCustom(schemaVersionKey, strconv.Itoa(version))
}
