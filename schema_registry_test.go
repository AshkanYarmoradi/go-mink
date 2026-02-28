package mink

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSchemaRegistry_Register(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(r *SchemaRegistry) error
		wantErr bool
		errMsg  string
	}{
		{
			name: "register valid schema",
			setup: func(r *SchemaRegistry) error {
				return r.Register("OrderCreated", SchemaDefinition{
					Version: 1,
					Fields: []FieldDefinition{
						{Name: "order_id", Type: "string", Required: true},
						{Name: "amount", Type: "float64", Required: true},
					},
				})
			},
		},
		{
			name: "register multiple versions",
			setup: func(r *SchemaRegistry) error {
				if err := r.Register("OrderCreated", SchemaDefinition{
					Version: 1,
					Fields:  []FieldDefinition{{Name: "order_id", Type: "string", Required: true}},
				}); err != nil {
					return err
				}
				return r.Register("OrderCreated", SchemaDefinition{
					Version: 2,
					Fields: []FieldDefinition{
						{Name: "order_id", Type: "string", Required: true},
						{Name: "currency", Type: "string", Required: false},
					},
				})
			},
		},
		{
			name: "reject duplicate version",
			setup: func(r *SchemaRegistry) error {
				if err := r.Register("OrderCreated", SchemaDefinition{
					Version: 1,
					Fields:  []FieldDefinition{{Name: "order_id", Type: "string", Required: true}},
				}); err != nil {
					return err
				}
				return r.Register("OrderCreated", SchemaDefinition{
					Version: 1,
					Fields:  []FieldDefinition{{Name: "order_id", Type: "string", Required: true}},
				})
			},
			wantErr: true,
			errMsg:  "already registered",
		},
		{
			name: "reject version less than 1",
			setup: func(r *SchemaRegistry) error {
				return r.Register("OrderCreated", SchemaDefinition{Version: 0})
			},
			wantErr: true,
			errMsg:  "must be >= 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewSchemaRegistry()
			err := tt.setup(r)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errMsg)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSchemaRegistry_GetSchema(t *testing.T) {
	r := NewSchemaRegistry()
	_ = r.Register("OrderCreated", SchemaDefinition{
		Version: 1,
		Fields:  []FieldDefinition{{Name: "order_id", Type: "string", Required: true}},
	})

	t.Run("get existing schema", func(t *testing.T) {
		schema, err := r.GetSchema("OrderCreated", 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if schema.Version != 1 {
			t.Errorf("expected version 1, got %d", schema.Version)
		}
		if len(schema.Fields) != 1 {
			t.Errorf("expected 1 field, got %d", len(schema.Fields))
		}
	})

	t.Run("schema not found for unknown event type", func(t *testing.T) {
		_, err := r.GetSchema("Unknown", 1)
		if !errors.Is(err, ErrSchemaNotFound) {
			t.Errorf("expected ErrSchemaNotFound, got %v", err)
		}
	})

	t.Run("schema not found for unknown version", func(t *testing.T) {
		_, err := r.GetSchema("OrderCreated", 99)
		if !errors.Is(err, ErrSchemaNotFound) {
			t.Errorf("expected ErrSchemaNotFound, got %v", err)
		}
	})
}

func TestSchemaRegistry_GetLatestVersion(t *testing.T) {
	r := NewSchemaRegistry()

	t.Run("not found for unknown event type", func(t *testing.T) {
		_, err := r.GetLatestVersion("Unknown")
		if !errors.Is(err, ErrSchemaNotFound) {
			t.Errorf("expected ErrSchemaNotFound, got %v", err)
		}
	})

	t.Run("returns highest version", func(t *testing.T) {
		_ = r.Register("OrderCreated", SchemaDefinition{Version: 1})
		_ = r.Register("OrderCreated", SchemaDefinition{Version: 3})
		_ = r.Register("OrderCreated", SchemaDefinition{Version: 2})

		v, err := r.GetLatestVersion("OrderCreated")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != 3 {
			t.Errorf("expected 3, got %d", v)
		}
	})
}

func TestSchemaRegistry_CheckCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		v1     SchemaDefinition
		v2     SchemaDefinition
		expect SchemaCompatibility
	}{
		{
			name: "fully compatible - same fields",
			v1: SchemaDefinition{
				Version: 1,
				Fields: []FieldDefinition{
					{Name: "order_id", Type: "string", Required: true},
					{Name: "amount", Type: "float64", Required: true},
				},
			},
			v2: SchemaDefinition{
				Version: 2,
				Fields: []FieldDefinition{
					{Name: "order_id", Type: "string", Required: true},
					{Name: "amount", Type: "float64", Required: true},
				},
			},
			expect: SchemaFullyCompatible,
		},
		{
			name: "backward compatible - field added",
			v1: SchemaDefinition{
				Version: 1,
				Fields: []FieldDefinition{
					{Name: "order_id", Type: "string", Required: true},
				},
			},
			v2: SchemaDefinition{
				Version: 2,
				Fields: []FieldDefinition{
					{Name: "order_id", Type: "string", Required: true},
					{Name: "currency", Type: "string", Required: false},
				},
			},
			expect: SchemaBackwardCompatible,
		},
		{
			name: "forward compatible - field removed",
			v1: SchemaDefinition{
				Version: 1,
				Fields: []FieldDefinition{
					{Name: "order_id", Type: "string", Required: true},
					{Name: "legacy", Type: "string", Required: false},
				},
			},
			v2: SchemaDefinition{
				Version: 2,
				Fields: []FieldDefinition{
					{Name: "order_id", Type: "string", Required: true},
				},
			},
			expect: SchemaForwardCompatible,
		},
		{
			name: "breaking - field type changed",
			v1: SchemaDefinition{
				Version: 1,
				Fields: []FieldDefinition{
					{Name: "amount", Type: "float64", Required: true},
				},
			},
			v2: SchemaDefinition{
				Version: 2,
				Fields: []FieldDefinition{
					{Name: "amount", Type: "string", Required: true},
				},
			},
			expect: SchemaBreaking,
		},
		{
			name: "breaking - field added and removed",
			v1: SchemaDefinition{
				Version: 1,
				Fields: []FieldDefinition{
					{Name: "old_field", Type: "string", Required: true},
				},
			},
			v2: SchemaDefinition{
				Version: 2,
				Fields: []FieldDefinition{
					{Name: "new_field", Type: "string", Required: true},
				},
			},
			expect: SchemaBreaking,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewSchemaRegistry()
			_ = r.Register("TestEvent", tt.v1)
			_ = r.Register("TestEvent", tt.v2)

			compat, err := r.CheckCompatibility("TestEvent", tt.v1.Version, tt.v2.Version)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if compat != tt.expect {
				t.Errorf("expected %s, got %s", tt.expect, compat)
			}
		})
	}
}

func TestSchemaRegistry_RegisteredEventTypes(t *testing.T) {
	r := NewSchemaRegistry()

	types := r.RegisteredEventTypes()
	if len(types) != 0 {
		t.Errorf("expected empty, got %v", types)
	}

	_ = r.Register("OrderCreated", SchemaDefinition{Version: 1})
	_ = r.Register("OrderShipped", SchemaDefinition{Version: 1})

	types = r.RegisteredEventTypes()
	if len(types) != 2 {
		t.Fatalf("expected 2 types, got %d", len(types))
	}
	if types[0] != "OrderCreated" || types[1] != "OrderShipped" {
		t.Errorf("expected [OrderCreated, OrderShipped], got %v", types)
	}
}

func TestSchemaCompatibility_String(t *testing.T) {
	tests := []struct {
		compat SchemaCompatibility
		want   string
	}{
		{SchemaFullyCompatible, "fully-compatible"},
		{SchemaBackwardCompatible, "backward-compatible"},
		{SchemaForwardCompatible, "forward-compatible"},
		{SchemaBreaking, "breaking"},
		{SchemaCompatibility(99), "unknown(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.compat.String(); got != tt.want {
				t.Errorf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestSchemaDefinition_WithJSONSchema(t *testing.T) {
	r := NewSchemaRegistry()
	jsonSchema := json.RawMessage(`{"type": "object", "properties": {"order_id": {"type": "string"}}}`)
	err := r.Register("OrderCreated", SchemaDefinition{
		Version:    1,
		Fields:     []FieldDefinition{{Name: "order_id", Type: "string", Required: true}},
		JSONSchema: jsonSchema,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	schema, err := r.GetSchema("OrderCreated", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema.JSONSchema == nil {
		t.Error("expected JSONSchema to be set")
	}
}
