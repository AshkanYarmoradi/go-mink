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

func TestUpcastingSerializer_Serialize(t *testing.T) {
	inner := NewJSONSerializer()
	chain := NewUpcasterChain()
	s := NewUpcastingSerializer(inner, chain)

	event := OrderCreatedV2{OrderID: "order-1", Amount: 100, Currency: "USD"}
	data, err := s.Serialize(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify it produces the same output as the inner serializer
	expected, _ := inner.Serialize(event)
	if string(data) != string(expected) {
		t.Errorf("expected %s, got %s", expected, data)
	}
}

func TestUpcastingSerializer_Deserialize_NoUpcasters(t *testing.T) {
	inner := NewJSONSerializer()
	inner.Register("OrderCreatedV2", OrderCreatedV2{})
	chain := NewUpcasterChain()
	s := NewUpcastingSerializer(inner, chain)

	data, _ := json.Marshal(OrderCreatedV2{OrderID: "order-1", Amount: 100, Currency: "USD"})
	result, err := s.Deserialize(data, "OrderCreatedV2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	event, ok := result.(OrderCreatedV2)
	if !ok {
		t.Fatalf("expected OrderCreatedV2, got %T", result)
	}
	if event.OrderID != "order-1" {
		t.Errorf("expected order-1, got %s", event.OrderID)
	}
	if event.Currency != "USD" {
		t.Errorf("expected USD, got %s", event.Currency)
	}
}

func TestUpcastingSerializer_Deserialize_WithUpcasting(t *testing.T) {
	inner := NewJSONSerializer()
	inner.Register("OrderCreated", OrderCreatedV2{})

	chain := NewUpcasterChain()
	_ = chain.Register(newTestUpcaster("OrderCreated", 1, 2, func(data []byte, m Metadata) ([]byte, error) {
		var obj map[string]interface{}
		if err := json.Unmarshal(data, &obj); err != nil {
			return nil, err
		}
		obj["currency"] = "USD"
		return json.Marshal(obj)
	}))

	s := NewUpcastingSerializer(inner, chain)

	// Serialize a v1 event (no currency field)
	v1Data, _ := json.Marshal(OrderCreatedV1{OrderID: "order-1", Amount: 100})

	// Deserialize should upcast from v1 to v2
	result, err := s.Deserialize(v1Data, "OrderCreated")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	event, ok := result.(OrderCreatedV2)
	if !ok {
		t.Fatalf("expected OrderCreatedV2, got %T", result)
	}
	if event.Currency != "USD" {
		t.Errorf("expected USD after upcast, got %s", event.Currency)
	}
	if event.Amount != 100 {
		t.Errorf("expected amount 100, got %f", event.Amount)
	}
}

func TestUpcastingSerializer_DeserializeWithVersion(t *testing.T) {
	inner := NewJSONSerializer()
	inner.Register("OrderCreated", OrderCreatedV3{})

	chain := NewUpcasterChain()
	_ = chain.Register(newTestUpcaster("OrderCreated", 1, 2, func(data []byte, m Metadata) ([]byte, error) {
		var obj map[string]interface{}
		if err := json.Unmarshal(data, &obj); err != nil {
			return nil, err
		}
		obj["currency"] = "USD"
		return json.Marshal(obj)
	}))
	_ = chain.Register(newTestUpcaster("OrderCreated", 2, 3, func(data []byte, m Metadata) ([]byte, error) {
		var obj map[string]interface{}
		if err := json.Unmarshal(data, &obj); err != nil {
			return nil, err
		}
		obj["tax"] = 10.0
		return json.Marshal(obj)
	}))

	s := NewUpcastingSerializer(inner, chain)

	t.Run("upcast from v1 to v3", func(t *testing.T) {
		v1Data, _ := json.Marshal(OrderCreatedV1{OrderID: "order-1", Amount: 100})
		result, err := s.DeserializeWithVersion(v1Data, "OrderCreated", 1, Metadata{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		event := result.(OrderCreatedV3)
		if event.Currency != "USD" {
			t.Errorf("expected USD, got %s", event.Currency)
		}
		if event.Tax != 10.0 {
			t.Errorf("expected tax 10.0, got %f", event.Tax)
		}
	})

	t.Run("upcast from v2 to v3", func(t *testing.T) {
		v2Data, _ := json.Marshal(OrderCreatedV2{OrderID: "order-1", Amount: 100, Currency: "EUR"})
		result, err := s.DeserializeWithVersion(v2Data, "OrderCreated", 2, Metadata{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		event := result.(OrderCreatedV3)
		if event.Currency != "EUR" {
			t.Errorf("expected EUR (preserved), got %s", event.Currency)
		}
		if event.Tax != 10.0 {
			t.Errorf("expected tax 10.0, got %f", event.Tax)
		}
	})

	t.Run("v3 data no upcasting needed", func(t *testing.T) {
		v3Data, _ := json.Marshal(OrderCreatedV3{OrderID: "order-1", Amount: 100, Currency: "GBP", Tax: 20.0})
		result, err := s.DeserializeWithVersion(v3Data, "OrderCreated", 3, Metadata{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		event := result.(OrderCreatedV3)
		if event.Currency != "GBP" {
			t.Errorf("expected GBP, got %s", event.Currency)
		}
		if event.Tax != 20.0 {
			t.Errorf("expected tax 20.0, got %f", event.Tax)
		}
	})
}

func TestUpcastingSerializer_DeserializeWithVersion_ErrorPropagation(t *testing.T) {
	inner := NewJSONSerializer()
	inner.Register("OrderCreated", OrderCreatedV2{})

	chain := NewUpcasterChain()
	_ = chain.Register(newTestUpcaster("OrderCreated", 1, 2, func(data []byte, m Metadata) ([]byte, error) {
		return nil, errors.New("upcast failed")
	}))

	s := NewUpcastingSerializer(inner, chain)

	v1Data, _ := json.Marshal(OrderCreatedV1{OrderID: "order-1", Amount: 100})
	_, err := s.DeserializeWithVersion(v1Data, "OrderCreated", 1, Metadata{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
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

	event, ok := result.(OrderCreatedV1)
	if !ok {
		t.Fatalf("expected OrderCreatedV1, got %T", result)
	}
	if event.OrderID != "order-1" {
		t.Errorf("expected order-1, got %s", event.OrderID)
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
	// This test verifies the compile-time interface check
	var _ Serializer = (*UpcastingSerializer)(nil)
}
