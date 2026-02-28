package mink

import (
	"encoding/json"
	"errors"
	"testing"
)

type OrderCreatedV1 struct {
	OrderID string  `json:"order_id"`
	Amount  float64 `json:"amount"`
}

type OrderCreatedV2 struct {
	OrderID  string  `json:"order_id"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type OrderCreatedV3 struct {
	OrderID  string  `json:"order_id"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Tax      float64 `json:"tax"`
}

// newOrderCreatedChain builds a reusable v1→v2→v3 upcaster chain for OrderCreated tests.
func newOrderCreatedChain() *UpcasterChain {
	chain := NewUpcasterChain()
	_ = chain.Register(newTestUpcaster("OrderCreated", 1, 2, addJSONFieldUpcastFn("currency", "USD")))
	_ = chain.Register(newTestUpcaster("OrderCreated", 2, 3, addJSONFieldUpcastFn("tax", 10.0)))
	return chain
}

func TestUpcastingSerializer_Serialize(t *testing.T) {
	inner := NewJSONSerializer()
	s := NewUpcastingSerializer(inner, NewUpcasterChain())

	event := OrderCreatedV2{OrderID: "order-1", Amount: 100, Currency: "USD"}
	data, err := s.Serialize(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected, _ := inner.Serialize(event)
	if string(data) != string(expected) {
		t.Errorf("expected %s, got %s", expected, data)
	}
}

func TestUpcastingSerializer_Deserialize_NoUpcasters(t *testing.T) {
	inner := NewJSONSerializer()
	inner.Register("OrderCreatedV2", OrderCreatedV2{})
	s := NewUpcastingSerializer(inner, NewUpcasterChain())

	data, _ := json.Marshal(OrderCreatedV2{OrderID: "order-1", Amount: 100, Currency: "USD"})
	result, err := s.Deserialize(data, "OrderCreatedV2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	event, ok := result.(OrderCreatedV2)
	if !ok {
		t.Fatalf("expected OrderCreatedV2, got %T", result)
	}
	if event.OrderID != "order-1" || event.Currency != "USD" {
		t.Errorf("unexpected event values: %+v", event)
	}
}

func TestUpcastingSerializer_Deserialize_WithUpcasting(t *testing.T) {
	inner := NewJSONSerializer()
	inner.Register("OrderCreated", OrderCreatedV2{})

	chain := NewUpcasterChain()
	_ = chain.Register(newTestUpcaster("OrderCreated", 1, 2, addJSONFieldUpcastFn("currency", "USD")))
	s := NewUpcastingSerializer(inner, chain)

	v1Data, _ := json.Marshal(OrderCreatedV1{OrderID: "order-1", Amount: 100})
	result, err := s.Deserialize(v1Data, "OrderCreated")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	event, ok := result.(OrderCreatedV2)
	if !ok {
		t.Fatalf("expected OrderCreatedV2, got %T", result)
	}
	if event.Currency != "USD" || event.Amount != 100 {
		t.Errorf("unexpected event values: %+v", event)
	}
}

func TestUpcastingSerializer_DeserializeWithVersion(t *testing.T) {
	inner := NewJSONSerializer()
	inner.Register("OrderCreated", OrderCreatedV3{})
	s := NewUpcastingSerializer(inner, newOrderCreatedChain())

	tests := []struct {
		name         string
		data         interface{}
		fromVersion  int
		wantCurrency string
		wantTax      float64
	}{
		{"v1 to v3", OrderCreatedV1{OrderID: "o1", Amount: 100}, 1, "USD", 10.0},
		{"v2 to v3", OrderCreatedV2{OrderID: "o1", Amount: 100, Currency: "EUR"}, 2, "EUR", 10.0},
		{"v3 no upcast", OrderCreatedV3{OrderID: "o1", Amount: 100, Currency: "GBP", Tax: 20.0}, 3, "GBP", 20.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.data)
			result, err := s.DeserializeWithVersion(data, "OrderCreated", tt.fromVersion, Metadata{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			event := result.(OrderCreatedV3)
			if event.Currency != tt.wantCurrency {
				t.Errorf("expected currency %s, got %s", tt.wantCurrency, event.Currency)
			}
			if event.Tax != tt.wantTax {
				t.Errorf("expected tax %v, got %v", tt.wantTax, event.Tax)
			}
		})
	}
}

func TestUpcastingSerializer_DeserializeWithVersion_ErrorPropagation(t *testing.T) {
	inner := NewJSONSerializer()
	inner.Register("OrderCreated", OrderCreatedV2{})

	chain := NewUpcasterChain()
	_ = chain.Register(newTestUpcaster("OrderCreated", 1, 2, func(data []byte, _ Metadata) ([]byte, error) {
		return nil, errors.New("upcast failed")
	}))

	s := NewUpcastingSerializer(inner, chain)
	v1Data, _ := json.Marshal(OrderCreatedV1{OrderID: "order-1", Amount: 100})
	_, err := s.DeserializeWithVersion(v1Data, "OrderCreated", 1, Metadata{})
	if !errors.Is(err, ErrUpcastFailed) {
		t.Errorf("expected ErrUpcastFailed, got %v", err)
	}
}

func TestUpcastingSerializer_NilChain(t *testing.T) {
	inner := NewJSONSerializer()
	inner.Register("OrderCreated", OrderCreatedV1{})
	s := NewUpcastingSerializer(inner, nil)

	data, _ := json.Marshal(OrderCreatedV1{OrderID: "order-1", Amount: 100})
	result, err := s.Deserialize(data, "OrderCreated")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event, ok := result.(OrderCreatedV1); !ok || event.OrderID != "order-1" {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestUpcastingSerializer_Inner(t *testing.T) {
	inner := NewJSONSerializer()
	chain := NewUpcasterChain()
	s := NewUpcastingSerializer(inner, chain)

	if s.Inner() != inner {
		t.Error("Inner() should return the wrapped serializer")
	}
	if s.Chain() != chain {
		t.Error("Chain() should return the upcaster chain")
	}
}

func TestUpcastingSerializer_CompileTimeCheck(t *testing.T) {
	var _ Serializer = (*UpcastingSerializer)(nil)
}
