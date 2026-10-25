package main

// OrderCreated records who placed an order.
type OrderCreated struct {
	CustomerID string `json:"customerId"`
}

// ItemAdded records an addition to an order.
type ItemAdded struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

// OrderSummary is a read model rebuilt from the event stream.
type OrderSummary struct {
	CustomerID string
	Items      int
}
