package mink

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// testUpcaster is a simple upcaster for testing.
type testUpcaster struct {
	eventType   string
	fromVersion int
	toVersion   int
	upcastFn    func(data []byte, metadata Metadata) ([]byte, error)
}

func (u *testUpcaster) EventType() string  { return u.eventType }
func (u *testUpcaster) FromVersion() int   { return u.fromVersion }
func (u *testUpcaster) ToVersion() int     { return u.toVersion }
func (u *testUpcaster) Upcast(data []byte, metadata Metadata) ([]byte, error) {
	return u.upcastFn(data, metadata)
}

func newTestUpcaster(eventType string, from, to int, fn func([]byte, Metadata) ([]byte, error)) *testUpcaster {
	return &testUpcaster{
		eventType:   eventType,
		fromVersion: from,
		toVersion:   to,
		upcastFn:    fn,
	}
}

// noopUpcastFn is a reusable no-op upcast function for tests that only need registration.
var noopUpcastFn = func(data []byte, _ Metadata) ([]byte, error) { return data, nil }

// newNoopUpcaster creates a test upcaster that passes data through unchanged.
func newNoopUpcaster(eventType string, from, to int) *testUpcaster {
	return newTestUpcaster(eventType, from, to, noopUpcastFn)
}

// addJSONFieldUpcastFn returns an upcast function that adds a field with a static value.
func addJSONFieldUpcastFn(field string, value interface{}) func([]byte, Metadata) ([]byte, error) {
	return func(data []byte, _ Metadata) ([]byte, error) {
		var obj map[string]interface{}
		if err := json.Unmarshal(data, &obj); err != nil {
			return nil, err
		}
		obj[field] = value
		return json.Marshal(obj)
	}
}

func TestUpcasterChain_Register(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(c *UpcasterChain) error
		wantErr bool
		errMsg  string
	}{
		{
			name: "register valid upcaster",
			setup: func(c *UpcasterChain) error {
				return c.Register(newNoopUpcaster("OrderCreated", 1, 2))
			},
		},
		{
			name: "register multiple upcasters for same event type",
			setup: func(c *UpcasterChain) error {
				if err := c.Register(newNoopUpcaster("OrderCreated", 1, 2)); err != nil {
					return err
				}
				return c.Register(newNoopUpcaster("OrderCreated", 2, 3))
			},
		},
		{
			name: "register upcasters for different event types",
			setup: func(c *UpcasterChain) error {
				if err := c.Register(newNoopUpcaster("OrderCreated", 1, 2)); err != nil {
					return err
				}
				return c.Register(newNoopUpcaster("OrderShipped", 1, 2))
			},
		},
		{
			name: "reject invalid version transition",
			setup: func(c *UpcasterChain) error {
				return c.Register(newNoopUpcaster("OrderCreated", 1, 3))
			},
			wantErr: true,
			errMsg:  "ToVersion (3) == FromVersion (1) + 1",
		},
		{
			name: "reject duplicate registration",
			setup: func(c *UpcasterChain) error {
				if err := c.Register(newNoopUpcaster("OrderCreated", 1, 2)); err != nil {
					return err
				}
				return c.Register(newNoopUpcaster("OrderCreated", 1, 2))
			},
			wantErr: true,
			errMsg:  "duplicate upcaster",
		},
		{
			name: "reject FromVersion less than 1",
			setup: func(c *UpcasterChain) error {
				return c.Register(newNoopUpcaster("OrderCreated", 0, 1))
			},
			wantErr: true,
			errMsg:  "FromVersion >= 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain := NewUpcasterChain()
			err := tt.setup(chain)
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

func TestUpcasterChain_Validate(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(c *UpcasterChain)
		wantErr error
	}{
		{
			name:  "empty chain is valid",
			setup: func(c *UpcasterChain) {},
		},
		{
			name: "contiguous chain is valid",
			setup: func(c *UpcasterChain) {
				_ = c.Register(newNoopUpcaster("OrderCreated", 1, 2))
				_ = c.Register(newNoopUpcaster("OrderCreated", 2, 3))
				_ = c.Register(newNoopUpcaster("OrderCreated", 3, 4))
			},
		},
		{
			name: "gap in chain is invalid",
			setup: func(c *UpcasterChain) {
				_ = c.Register(newNoopUpcaster("OrderCreated", 1, 2))
				// Skip v2→v3
				_ = c.Register(newNoopUpcaster("OrderCreated", 3, 4))
			},
			wantErr: ErrSchemaVersionGap,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain := NewUpcasterChain()
			tt.setup(chain)
			err := chain.Validate()
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("expected error %v, got %v", tt.wantErr, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestUpcasterChain_Upcast(t *testing.T) {
	t.Run("no upcasters returns data unchanged", func(t *testing.T) {
		chain := NewUpcasterChain()
		data := []byte(`{"name":"test"}`)
		result, version, err := chain.Upcast("OrderCreated", 1, data, Metadata{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if version != 1 {
			t.Errorf("expected version 1, got %d", version)
		}
		if string(result) != string(data) {
			t.Errorf("expected data unchanged, got %s", result)
		}
	})

	t.Run("single upcaster v1 to v2", func(t *testing.T) {
		chain := NewUpcasterChain()
		_ = chain.Register(newTestUpcaster("OrderCreated", 1, 2, addJSONFieldUpcastFn("currency", "USD")))

		result, version, err := chain.Upcast("OrderCreated", 1, []byte(`{"amount":100}`), Metadata{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if version != 2 {
			t.Errorf("expected version 2, got %d", version)
		}
		assertJSONField(t, result, "currency", "USD")
	})

	t.Run("chain v1 to v2 to v3", func(t *testing.T) {
		chain := NewUpcasterChain()
		_ = chain.Register(newTestUpcaster("OrderCreated", 1, 2, addJSONFieldUpcastFn("currency", "USD")))
		_ = chain.Register(newTestUpcaster("OrderCreated", 2, 3, addJSONFieldUpcastFn("tax", 0.0)))

		result, version, err := chain.Upcast("OrderCreated", 1, []byte(`{"amount":100}`), Metadata{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if version != 3 {
			t.Errorf("expected version 3, got %d", version)
		}
		assertJSONField(t, result, "currency", "USD")
		assertJSONField(t, result, "tax", 0.0)
	})

	t.Run("upcast from v2 skips v1 upcaster", func(t *testing.T) {
		chain := NewUpcasterChain()
		v1Called := false
		_ = chain.Register(newTestUpcaster("OrderCreated", 1, 2, func(data []byte, _ Metadata) ([]byte, error) {
			v1Called = true
			return data, nil
		}))
		_ = chain.Register(newTestUpcaster("OrderCreated", 2, 3, addJSONFieldUpcastFn("tax", 0.0)))

		result, version, err := chain.Upcast("OrderCreated", 2, []byte(`{"amount":100,"currency":"EUR"}`), Metadata{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v1Called {
			t.Error("v1→v2 upcaster should not have been called")
		}
		if version != 3 {
			t.Errorf("expected version 3, got %d", version)
		}
		assertJSONField(t, result, "currency", "EUR")
	})

	t.Run("metadata context passed to upcaster", func(t *testing.T) {
		chain := NewUpcasterChain()
		_ = chain.Register(newTestUpcaster("OrderCreated", 1, 2, func(data []byte, m Metadata) ([]byte, error) {
			currency := "USD"
			if m.TenantID == "eu-tenant" {
				currency = "EUR"
			}
			return addJSONFieldUpcastFn("currency", currency)(data, m)
		}))

		data := []byte(`{"amount":100}`)

		result, _, err := chain.Upcast("OrderCreated", 1, data, Metadata{TenantID: "eu-tenant"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertJSONField(t, result, "currency", "EUR")

		result, _, err = chain.Upcast("OrderCreated", 1, data, Metadata{TenantID: "us-tenant"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertJSONField(t, result, "currency", "USD")
	})

	t.Run("upcaster error propagation", func(t *testing.T) {
		chain := NewUpcasterChain()
		_ = chain.Register(newTestUpcaster("OrderCreated", 1, 2, func(data []byte, m Metadata) ([]byte, error) {
			return nil, fmt.Errorf("transformation failed")
		}))

		_, _, err := chain.Upcast("OrderCreated", 1, []byte(`{}`), Metadata{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrUpcastFailed) {
			t.Errorf("expected ErrUpcastFailed, got %v", err)
		}

		var upcastErr *UpcastError
		if !errors.As(err, &upcastErr) {
			t.Fatal("expected UpcastError type")
		}
		if upcastErr.EventType != "OrderCreated" {
			t.Errorf("expected event type OrderCreated, got %s", upcastErr.EventType)
		}
		if upcastErr.FromVersion != 1 {
			t.Errorf("expected from version 1, got %d", upcastErr.FromVersion)
		}
		if upcastErr.ToVersion != 2 {
			t.Errorf("expected to version 2, got %d", upcastErr.ToVersion)
		}
	})
}

func TestUpcasterChain_HasUpcasters(t *testing.T) {
	chain := NewUpcasterChain()

	if chain.HasUpcasters("OrderCreated") {
		t.Error("expected no upcasters for OrderCreated")
	}

	_ = chain.Register(newNoopUpcaster("OrderCreated", 1, 2))

	if !chain.HasUpcasters("OrderCreated") {
		t.Error("expected upcasters for OrderCreated")
	}
	if chain.HasUpcasters("OrderShipped") {
		t.Error("expected no upcasters for OrderShipped")
	}
}

func TestUpcasterChain_LatestVersion(t *testing.T) {
	chain := NewUpcasterChain()

	if v := chain.LatestVersion("OrderCreated"); v != DefaultSchemaVersion {
		t.Errorf("expected DefaultSchemaVersion, got %d", v)
	}

	_ = chain.Register(newNoopUpcaster("OrderCreated", 1, 2))
	if v := chain.LatestVersion("OrderCreated"); v != 2 {
		t.Errorf("expected 2, got %d", v)
	}

	_ = chain.Register(newNoopUpcaster("OrderCreated", 2, 3))
	if v := chain.LatestVersion("OrderCreated"); v != 3 {
		t.Errorf("expected 3, got %d", v)
	}
}

func TestUpcasterChain_RegisteredEventTypes(t *testing.T) {
	chain := NewUpcasterChain()

	if types := chain.RegisteredEventTypes(); len(types) != 0 {
		t.Errorf("expected empty, got %v", types)
	}

	_ = chain.Register(newNoopUpcaster("OrderCreated", 1, 2))
	_ = chain.Register(newNoopUpcaster("OrderShipped", 1, 2))

	types := chain.RegisteredEventTypes()
	if len(types) != 2 {
		t.Fatalf("expected 2 types, got %d", len(types))
	}
	if types[0] != "OrderCreated" || types[1] != "OrderShipped" {
		t.Errorf("expected [OrderCreated, OrderShipped], got %v", types)
	}
}

func TestGetSchemaVersion(t *testing.T) {
	tests := []struct {
		name     string
		metadata Metadata
		want     int
	}{
		{
			name:     "empty metadata returns default",
			metadata: Metadata{},
			want:     DefaultSchemaVersion,
		},
		{
			name:     "nil custom map returns default",
			metadata: Metadata{Custom: nil},
			want:     DefaultSchemaVersion,
		},
		{
			name:     "missing key returns default",
			metadata: Metadata{Custom: map[string]string{"other": "value"}},
			want:     DefaultSchemaVersion,
		},
		{
			name:     "invalid value returns default",
			metadata: Metadata{Custom: map[string]string{schemaVersionKey: "invalid"}},
			want:     DefaultSchemaVersion,
		},
		{
			name:     "valid version 1",
			metadata: Metadata{Custom: map[string]string{schemaVersionKey: "1"}},
			want:     1,
		},
		{
			name:     "valid version 3",
			metadata: Metadata{Custom: map[string]string{schemaVersionKey: "3"}},
			want:     3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetSchemaVersion(tt.metadata)
			if got != tt.want {
				t.Errorf("GetSchemaVersion() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSetSchemaVersion(t *testing.T) {
	t.Run("set version on empty metadata", func(t *testing.T) {
		m := SetSchemaVersion(Metadata{}, 2)
		if v := GetSchemaVersion(m); v != 2 {
			t.Errorf("expected version 2, got %d", v)
		}
	})

	t.Run("set version preserves existing metadata", func(t *testing.T) {
		m := Metadata{
			CorrelationID: "corr-123",
			Custom:        map[string]string{"tenant": "acme"},
		}
		m = SetSchemaVersion(m, 3)
		if v := GetSchemaVersion(m); v != 3 {
			t.Errorf("expected version 3, got %d", v)
		}
		if m.CorrelationID != "corr-123" {
			t.Errorf("expected correlation ID preserved, got %s", m.CorrelationID)
		}
		if m.Custom["tenant"] != "acme" {
			t.Errorf("expected tenant preserved, got %s", m.Custom["tenant"])
		}
	})

	t.Run("overwrite existing version", func(t *testing.T) {
		m := SetSchemaVersion(Metadata{}, 2)
		m = SetSchemaVersion(m, 5)
		if v := GetSchemaVersion(m); v != 5 {
			t.Errorf("expected version 5, got %d", v)
		}
	})
}

func TestVersioningErrors(t *testing.T) {
	t.Run("UpcastError matches ErrUpcastFailed", func(t *testing.T) {
		cause := fmt.Errorf("bad data")
		err := NewUpcastError("OrderCreated", 1, 2, cause)
		if !errors.Is(err, ErrUpcastFailed) {
			t.Error("expected UpcastError to match ErrUpcastFailed")
		}
		if err.Error() == "" {
			t.Error("expected non-empty error message")
		}
		if !strings.Contains(err.Error(), "OrderCreated") {
			t.Error("error message should contain event type")
		}
		if err.Unwrap() != cause {
			t.Error("Unwrap should return the cause")
		}
	})

	t.Run("SchemaVersionGapError matches ErrSchemaVersionGap", func(t *testing.T) {
		err := NewSchemaVersionGapError("OrderCreated", 2, 3)
		if !errors.Is(err, ErrSchemaVersionGap) {
			t.Error("expected SchemaVersionGapError to match ErrSchemaVersionGap")
		}
		if err.Error() == "" {
			t.Error("expected non-empty error message")
		}
		if !strings.Contains(err.Error(), "OrderCreated") {
			t.Error("error message should contain event type")
		}
		if err.Unwrap() != ErrSchemaVersionGap {
			t.Error("Unwrap should return ErrSchemaVersionGap")
		}
	})

	t.Run("IncompatibleSchemaError matches ErrIncompatibleSchema", func(t *testing.T) {
		err := NewIncompatibleSchemaError("OrderCreated", 1, 2, SchemaBreaking, "field removed")
		if !errors.Is(err, ErrIncompatibleSchema) {
			t.Error("expected IncompatibleSchemaError to match ErrIncompatibleSchema")
		}
		if err.Error() == "" {
			t.Error("expected non-empty error message")
		}
		if !strings.Contains(err.Error(), "OrderCreated") {
			t.Error("error message should contain event type")
		}
		if err.Unwrap() != ErrIncompatibleSchema {
			t.Error("Unwrap should return ErrIncompatibleSchema")
		}
	})
}

func TestSerializeEventWithVersion(t *testing.T) {
	serializer := NewJSONSerializer()

	t.Run("stamps schema version in metadata", func(t *testing.T) {
		type TestEvent struct {
			Name string `json:"name"`
		}
		eventData, err := SerializeEventWithVersion(serializer, TestEvent{Name: "hello"}, Metadata{}, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if eventData.Type != "TestEvent" {
			t.Errorf("expected type TestEvent, got %s", eventData.Type)
		}
		if v := GetSchemaVersion(eventData.Metadata); v != 3 {
			t.Errorf("expected schema version 3, got %d", v)
		}
	})

	t.Run("preserves existing metadata", func(t *testing.T) {
		type TestEvent struct {
			Name string `json:"name"`
		}
		m := Metadata{CorrelationID: "corr-1"}
		eventData, err := SerializeEventWithVersion(serializer, TestEvent{Name: "test"}, m, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if eventData.Metadata.CorrelationID != "corr-1" {
			t.Error("expected correlation ID preserved")
		}
		if v := GetSchemaVersion(eventData.Metadata); v != 2 {
			t.Errorf("expected schema version 2, got %d", v)
		}
	})

	t.Run("error on nil event", func(t *testing.T) {
		_, err := SerializeEventWithVersion(serializer, nil, Metadata{}, 1)
		if err == nil {
			t.Error("expected error for nil event")
		}
	})
}

func TestUpcasterChain_Validate_EmptyUpcasters(t *testing.T) {
	chain := NewUpcasterChain()
	_ = chain.Register(newNoopUpcaster("SingleEvent", 1, 2))
	if err := chain.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpcasterChain_Upcast_GapBreaks(t *testing.T) {
	chain := NewUpcasterChain()
	_ = chain.Register(newNoopUpcaster("GapEvent", 1, 2))
	_ = chain.Register(newNoopUpcaster("GapEvent", 3, 4)) // skip v2→v3

	data := []byte(`{"value":1}`)
	result, version, err := chain.Upcast("GapEvent", 2, data, Metadata{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != 2 {
		t.Errorf("expected version 2 (stopped at gap), got %d", version)
	}
	if string(result) != string(data) {
		t.Errorf("expected data unchanged at gap, got %s", result)
	}
}

// assertJSONField unmarshals JSON data and checks that the given field has the expected value.
func assertJSONField(t *testing.T, data []byte, field string, expected interface{}) {
	t.Helper()
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if obj[field] != expected {
		t.Errorf("expected %s=%v, got %v", field, expected, obj[field])
	}
}
