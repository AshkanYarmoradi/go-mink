package mink

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// SchemaCompatibility represents the level of backward compatibility between schema versions.
type SchemaCompatibility int

const (
	// SchemaFullyCompatible indicates the schema change is fully backward and forward compatible.
	// No fields were added, removed, or changed — only documentation or ordering changes.
	SchemaFullyCompatible SchemaCompatibility = iota

	// SchemaBackwardCompatible indicates old data can be read by new code.
	// Fields may have been added (with defaults) but none removed or changed.
	SchemaBackwardCompatible

	// SchemaForwardCompatible indicates new data can be read by old code.
	// Fields may have been removed but none added or changed.
	SchemaForwardCompatible

	// SchemaBreaking indicates the schema change breaks compatibility.
	// Fields were changed, renamed, or removed without migration support.
	SchemaBreaking
)

// String returns a human-readable name for the compatibility level.
func (c SchemaCompatibility) String() string {
	switch c {
	case SchemaFullyCompatible:
		return "fully-compatible"
	case SchemaBackwardCompatible:
		return "backward-compatible"
	case SchemaForwardCompatible:
		return "forward-compatible"
	case SchemaBreaking:
		return "breaking"
	default:
		return fmt.Sprintf("unknown(%d)", int(c))
	}
}

// FieldDefinition describes a single field in an event schema.
type FieldDefinition struct {
	// Name is the field name.
	Name string

	// Type is the field type (e.g., "string", "int", "bool").
	Type string

	// Required indicates whether the field must be present.
	Required bool
}

// SchemaDefinition describes the schema for a specific version of an event type.
type SchemaDefinition struct {
	// Version is the schema version number.
	Version int

	// Fields describes the fields in this schema version.
	Fields []FieldDefinition

	// JSONSchema is an optional JSON Schema document for validation.
	JSONSchema json.RawMessage

	// RegisteredAt is when this schema version was registered.
	RegisteredAt time.Time
}

// SchemaRegistry is an in-memory registry that tracks event schemas and their versions.
// It provides compatibility checking between schema versions.
type SchemaRegistry struct {
	mu      sync.RWMutex
	schemas map[string]map[int]*SchemaDefinition // eventType → version → schema
}

// NewSchemaRegistry creates a new empty SchemaRegistry.
func NewSchemaRegistry() *SchemaRegistry {
	return &SchemaRegistry{
		schemas: make(map[string]map[int]*SchemaDefinition),
	}
}

// Register adds a schema definition for an event type.
// Returns an error if the version is < 1 or already registered.
func (r *SchemaRegistry) Register(eventType string, schema SchemaDefinition) error {
	if schema.Version < 1 {
		return fmt.Errorf("mink: schema version must be >= 1, got %d", schema.Version)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	versions, ok := r.schemas[eventType]
	if !ok {
		versions = make(map[int]*SchemaDefinition)
		r.schemas[eventType] = versions
	}

	if _, exists := versions[schema.Version]; exists {
		return fmt.Errorf("mink: schema version %d already registered for event type %q",
			schema.Version, eventType)
	}

	s := schema
	if s.RegisteredAt.IsZero() {
		s.RegisteredAt = time.Now()
	}
	versions[schema.Version] = &s
	return nil
}

// GetSchema retrieves a specific schema version for an event type.
func (r *SchemaRegistry) GetSchema(eventType string, version int) (*SchemaDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	versions, ok := r.schemas[eventType]
	if !ok {
		return nil, fmt.Errorf("%w: event type %q", ErrSchemaNotFound, eventType)
	}

	schema, ok := versions[version]
	if !ok {
		return nil, fmt.Errorf("%w: event type %q version %d", ErrSchemaNotFound, eventType, version)
	}

	return schema, nil
}

// GetLatestVersion returns the highest registered schema version for an event type.
func (r *SchemaRegistry) GetLatestVersion(eventType string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	versions, ok := r.schemas[eventType]
	if !ok {
		return 0, fmt.Errorf("%w: event type %q", ErrSchemaNotFound, eventType)
	}

	latest := 0
	for v := range versions {
		if v > latest {
			latest = v
		}
	}
	return latest, nil
}

// CheckCompatibility determines the compatibility level between two schema versions.
// It compares the field definitions to classify the change.
func (r *SchemaRegistry) CheckCompatibility(eventType string, oldVersion, newVersion int) (SchemaCompatibility, error) {
	oldSchema, err := r.GetSchema(eventType, oldVersion)
	if err != nil {
		return SchemaBreaking, err
	}
	newSchema, err := r.GetSchema(eventType, newVersion)
	if err != nil {
		return SchemaBreaking, err
	}

	oldFields := make(map[string]FieldDefinition, len(oldSchema.Fields))
	for _, f := range oldSchema.Fields {
		oldFields[f.Name] = f
	}
	newFields := make(map[string]FieldDefinition, len(newSchema.Fields))
	for _, f := range newSchema.Fields {
		newFields[f.Name] = f
	}

	hasAddedFields := false
	hasRemovedFields := false
	hasChangedFields := false

	// Check for removed or changed fields
	for name, oldField := range oldFields {
		if newField, ok := newFields[name]; !ok {
			hasRemovedFields = true
		} else if oldField.Type != newField.Type {
			hasChangedFields = true
		}
	}

	// Check for added fields
	for name := range newFields {
		if _, ok := oldFields[name]; !ok {
			hasAddedFields = true
		}
	}

	if hasChangedFields {
		return SchemaBreaking, nil
	}
	if hasRemovedFields && hasAddedFields {
		return SchemaBreaking, nil
	}
	if hasRemovedFields {
		return SchemaForwardCompatible, nil
	}
	if hasAddedFields {
		return SchemaBackwardCompatible, nil
	}
	return SchemaFullyCompatible, nil
}

// RegisteredEventTypes returns a sorted list of event types that have schemas registered.
func (r *SchemaRegistry) RegisteredEventTypes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	types := make([]string, 0, len(r.schemas))
	for t := range r.schemas {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}
