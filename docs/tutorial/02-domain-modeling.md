---
layout: default
title: "Part 2: Domain Modeling"
parent: "Tutorial: Building an E-Commerce App"
nav_order: 2
permalink: /tutorial/02-domain-modeling
---

# Part 2: Domain Modeling
{: .no_toc }

Design your e-commerce domain with events, aggregates, and business rules.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

In this part, you'll:

- Design domain events that capture what happens in your system
- Build aggregate roots that enforce business rules
- Implement the event sourcing pattern with state reconstruction
- Handle common e-commerce scenarios

**Time**: ~45 minutes

---

## Thinking in Events

Before writing code, let's think about what **happens** in our e-commerce system:

### Product Lifecycle
```
ProductCreated → StockAdded → PriceChanged → StockReserved → StockReleased → ProductDiscontinued
```

### Shopping Cart Flow
```
CartCreated → ItemAdded → ItemQuantityChanged → ItemRemoved → CartCleared → CartAbandoned
```

### Order Process
```
OrderPlaced → PaymentReceived → OrderConfirmed → OrderShipped → OrderDelivered
                    ↓
              PaymentFailed → OrderCancelled
```

Notice how we describe **what happened**, not **what changed**. This is the key insight of Event Sourcing.

---

## Part 1: Product Aggregate

Let's build the Product aggregate with inventory management.

### Define Product Events

Create `internal/domain/product/events.go`:

```go
package product

import (
	"time"
)

// ProductCreated is emitted when a new product is added to the catalog.
type ProductCreated struct {
	ProductID    string    `json:"productId"`
	SKU          string    `json:"sku"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Price        float64   `json:"price"`
	InitialStock int       `json:"initialStock"`
	CreatedAt    time.Time `json:"createdAt"`
}

// PriceChanged is emitted when a product's price is updated.
type PriceChanged struct {
	ProductID string    `json:"productId"`
	OldPrice  float64   `json:"oldPrice"`
	NewPrice  float64   `json:"newPrice"`
	Reason    string    `json:"reason"` // "promotion", "cost_increase", etc.
	ChangedAt time.Time `json:"changedAt"`
}

// StockAdded is emitted when inventory is restocked.
type StockAdded struct {
	ProductID    string    `json:"productId"`
	Quantity     int       `json:"quantity"`
	Reference    string    `json:"reference"` // PO number, supplier ref
	AddedAt      time.Time `json:"addedAt"`
}

// StockReserved is emitted when stock is reserved for an order.
type StockReserved struct {
	ProductID  string    `json:"productId"`
	Quantity   int       `json:"quantity"`
	OrderID    string    `json:"orderId"`
	ReservedAt time.Time `json:"reservedAt"`
}

// StockReleased is emitted when a reservation is cancelled.
type StockReleased struct {
	ProductID  string    `json:"productId"`
	Quantity   int       `json:"quantity"`
	OrderID    string    `json:"orderId"`
	Reason     string    `json:"reason"` // "order_cancelled", "timeout"
	ReleasedAt time.Time `json:"releasedAt"`
}

// StockShipped is emitted when reserved stock is shipped.
type StockShipped struct {
	ProductID string    `json:"productId"`
	Quantity  int       `json:"quantity"`
	OrderID   string    `json:"orderId"`
	ShippedAt time.Time `json:"shippedAt"`
}

// ProductDiscontinued is emitted when a product is no longer sold.
type ProductDiscontinued struct {
	ProductID       string    `json:"productId"`
	Reason          string    `json:"reason"`
	DiscontinuedAt  time.Time `json:"discontinuedAt"`
}

// AllEvents returns all product event types for registration.
func AllEvents() []interface{} {
	return []interface{}{
		ProductCreated{},
		PriceChanged{},
		StockAdded{},
		StockReserved{},
		StockReleased{},
		StockShipped{},
		ProductDiscontinued{},
	}
}
```

### Define Domain Errors

Create `internal/domain/product/errors.go`:

```go
package product

import "errors"

// Domain errors for the product aggregate.
var (
	ErrProductAlreadyExists  = errors.New("product already exists")
	ErrProductNotFound       = errors.New("product not found")
	ErrProductDiscontinued   = errors.New("product is discontinued")
	ErrInsufficientStock     = errors.New("insufficient stock")
	ErrInvalidPrice          = errors.New("price must be positive")
	ErrInvalidQuantity       = errors.New("quantity must be positive")
	ErrInvalidSKU            = errors.New("SKU cannot be empty")
	ErrReservationNotFound   = errors.New("reservation not found")
)
```

### Build the Product Aggregate

Create `internal/domain/product/aggregate.go`:

```go
package product

import (
	"fmt"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
)

// Reservation tracks stock reserved for an order.
type Reservation struct {
	OrderID    string
	Quantity   int
	ReservedAt time.Time
}

// Product is the aggregate root for product management.
type Product struct {
	mink.AggregateBase

	// State
	SKU           string
	Name          string
	Description   string
	Price         float64
	
	// Inventory
	AvailableStock int                  // Stock available for sale
	ReservedStock  int                  // Stock reserved for orders
	Reservations   map[string]*Reservation // OrderID -> Reservation
	
	// Status
	IsDiscontinued bool
	CreatedAt      time.Time
}

// NewProduct creates a new Product aggregate.
func NewProduct(id string) *Product {
	p := &Product{
		Reservations: make(map[string]*Reservation),
	}
	p.SetID(id)
	p.SetType("Product")
	return p
}

// StreamID returns the event stream ID for this product.
func (p *Product) StreamID() string {
	return fmt.Sprintf("Product-%s", p.AggregateID())
}

// --- Commands (Business Operations) ---

// Create initializes a new product.
func (p *Product) Create(sku, name, description string, price float64, initialStock int) error {
	// Validate
	if p.Version() > 0 {
		return ErrProductAlreadyExists
	}
	if sku == "" {
		return ErrInvalidSKU
	}
	if price <= 0 {
		return ErrInvalidPrice
	}
	if initialStock < 0 {
		return ErrInvalidQuantity
	}

	// Apply event
	p.Apply(ProductCreated{
		ProductID:    p.AggregateID(),
		SKU:          sku,
		Name:         name,
		Description:  description,
		Price:        price,
		InitialStock: initialStock,
		CreatedAt:    time.Now(),
	})

	// Update state
	p.SKU = sku
	p.Name = name
	p.Description = description
	p.Price = price
	p.AvailableStock = initialStock
	p.CreatedAt = time.Now()

	return nil
}

// ChangePrice updates the product price.
func (p *Product) ChangePrice(newPrice float64, reason string) error {
	if p.Version() == 0 {
		return ErrProductNotFound
	}
	if p.IsDiscontinued {
		return ErrProductDiscontinued
	}
	if newPrice <= 0 {
		return ErrInvalidPrice
	}
	if newPrice == p.Price {
		return nil // No change needed
	}

	oldPrice := p.Price
	p.Apply(PriceChanged{
		ProductID: p.AggregateID(),
		OldPrice:  oldPrice,
		NewPrice:  newPrice,
		Reason:    reason,
		ChangedAt: time.Now(),
	})

	p.Price = newPrice
	return nil
}

// AddStock increases available inventory.
func (p *Product) AddStock(quantity int, reference string) error {
	if p.Version() == 0 {
		return ErrProductNotFound
	}
	if p.IsDiscontinued {
		return ErrProductDiscontinued
	}
	if quantity <= 0 {
		return ErrInvalidQuantity
	}

	p.Apply(StockAdded{
		ProductID: p.AggregateID(),
		Quantity:  quantity,
		Reference: reference,
		AddedAt:   time.Now(),
	})

	p.AvailableStock += quantity
	return nil
}

// ReserveStock reserves inventory for an order.
func (p *Product) ReserveStock(orderID string, quantity int) error {
	if p.Version() == 0 {
		return ErrProductNotFound
	}
	if p.IsDiscontinued {
		return ErrProductDiscontinued
	}
	if quantity <= 0 {
		return ErrInvalidQuantity
	}
	if quantity > p.AvailableStock {
		return ErrInsufficientStock
	}

	p.Apply(StockReserved{
		ProductID:  p.AggregateID(),
		Quantity:   quantity,
		OrderID:    orderID,
		ReservedAt: time.Now(),
	})

	p.AvailableStock -= quantity
	p.ReservedStock += quantity
	p.Reservations[orderID] = &Reservation{
		OrderID:    orderID,
		Quantity:   quantity,
		ReservedAt: time.Now(),
	}

	return nil
}

// ReleaseStock cancels a reservation and returns stock to available.
func (p *Product) ReleaseStock(orderID string, reason string) error {
	if p.Version() == 0 {
		return ErrProductNotFound
	}

	reservation, exists := p.Reservations[orderID]
	if !exists {
		return ErrReservationNotFound
	}

	p.Apply(StockReleased{
		ProductID:  p.AggregateID(),
		Quantity:   reservation.Quantity,
		OrderID:    orderID,
		Reason:     reason,
		ReleasedAt: time.Now(),
	})

	p.AvailableStock += reservation.Quantity
	p.ReservedStock -= reservation.Quantity
	delete(p.Reservations, orderID)

	return nil
}

// ShipStock confirms that reserved stock has been shipped.
func (p *Product) ShipStock(orderID string) error {
	if p.Version() == 0 {
		return ErrProductNotFound
	}

	reservation, exists := p.Reservations[orderID]
	if !exists {
		return ErrReservationNotFound
	}

	p.Apply(StockShipped{
		ProductID: p.AggregateID(),
		Quantity:  reservation.Quantity,
		OrderID:   orderID,
		ShippedAt: time.Now(),
	})

	p.ReservedStock -= reservation.Quantity
	delete(p.Reservations, orderID)

	return nil
}

// Discontinue marks the product as no longer for sale.
func (p *Product) Discontinue(reason string) error {
	if p.Version() == 0 {
		return ErrProductNotFound
	}
	if p.IsDiscontinued {
		return nil // Already discontinued
	}

	p.Apply(ProductDiscontinued{
		ProductID:      p.AggregateID(),
		Reason:         reason,
		DiscontinuedAt: time.Now(),
	})

	p.IsDiscontinued = true
	return nil
}

// --- Event Application (State Reconstruction) ---

// ApplyEvent reconstructs state from a historical event.
func (p *Product) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case ProductCreated:
		p.SKU = e.SKU
		p.Name = e.Name
		p.Description = e.Description
		p.Price = e.Price
		p.AvailableStock = e.InitialStock
		p.CreatedAt = e.CreatedAt

	case PriceChanged:
		p.Price = e.NewPrice

	case StockAdded:
		p.AvailableStock += e.Quantity

	case StockReserved:
		p.AvailableStock -= e.Quantity
		p.ReservedStock += e.Quantity
		if p.Reservations == nil {
			p.Reservations = make(map[string]*Reservation)
		}
		p.Reservations[e.OrderID] = &Reservation{
			OrderID:    e.OrderID,
			Quantity:   e.Quantity,
			ReservedAt: e.ReservedAt,
		}

	case StockReleased:
		if res, ok := p.Reservations[e.OrderID]; ok {
			p.AvailableStock += res.Quantity
			p.ReservedStock -= res.Quantity
			delete(p.Reservations, e.OrderID)
		}

	case StockShipped:
		if res, ok := p.Reservations[e.OrderID]; ok {
			p.ReservedStock -= res.Quantity
			delete(p.Reservations, e.OrderID)
		}

	case ProductDiscontinued:
		p.IsDiscontinued = true

	default:
		return fmt.Errorf("unknown event type: %T", event)
	}

	p.IncrementVersion()
	return nil
}

// --- Query Methods ---

// TotalStock returns total stock (available + reserved).
func (p *Product) TotalStock() int {
	return p.AvailableStock + p.ReservedStock
}

// IsAvailable returns true if the product can be purchased.
func (p *Product) IsAvailable() bool {
	return !p.IsDiscontinued && p.AvailableStock > 0
}

// CanReserve returns true if the requested quantity can be reserved.
func (p *Product) CanReserve(quantity int) bool {
	return !p.IsDiscontinued && p.AvailableStock >= quantity
}
```

---

## Part 2: Shopping Cart Aggregate

Now let's build the shopping cart with item management.

### Define Cart Events

Create `internal/domain/cart/events.go`:

```go
package cart

import (
	"time"
)

// CartItem represents an item in the cart.
type CartItem struct {
	ProductID   string  `json:"productId"`
	ProductName string  `json:"productName"`
	Quantity    int     `json:"quantity"`
	UnitPrice   float64 `json:"unitPrice"`
}

// CartCreated is emitted when a new shopping cart is created.
type CartCreated struct {
	CartID     string    `json:"cartId"`
	CustomerID string    `json:"customerId"`
	CreatedAt  time.Time `json:"createdAt"`
}

// ItemAddedToCart is emitted when an item is added to the cart.
type ItemAddedToCart struct {
	CartID      string    `json:"cartId"`
	ProductID   string    `json:"productId"`
	ProductName string    `json:"productName"`
	Quantity    int       `json:"quantity"`
	UnitPrice   float64   `json:"unitPrice"`
	AddedAt     time.Time `json:"addedAt"`
}

// ItemQuantityChanged is emitted when an item's quantity is updated.
type ItemQuantityChanged struct {
	CartID      string    `json:"cartId"`
	ProductID   string    `json:"productId"`
	OldQuantity int       `json:"oldQuantity"`
	NewQuantity int       `json:"newQuantity"`
	ChangedAt   time.Time `json:"changedAt"`
}

// ItemRemovedFromCart is emitted when an item is removed.
type ItemRemovedFromCart struct {
	CartID    string    `json:"cartId"`
	ProductID string    `json:"productId"`
	RemovedAt time.Time `json:"removedAt"`
}

// CartCleared is emitted when all items are removed.
type CartCleared struct {
	CartID    string    `json:"cartId"`
	Reason    string    `json:"reason"` // "checkout", "user_action"
	ClearedAt time.Time `json:"clearedAt"`
}

// CartCheckedOut is emitted when the cart proceeds to checkout.
type CartCheckedOut struct {
	CartID       string     `json:"cartId"`
	OrderID      string     `json:"orderId"`
	Items        []CartItem `json:"items"`
	TotalAmount  float64    `json:"totalAmount"`
	CheckedOutAt time.Time  `json:"checkedOutAt"`
}

// AllEvents returns all cart event types for registration.
func AllEvents() []interface{} {
	return []interface{}{
		CartCreated{},
		ItemAddedToCart{},
		ItemQuantityChanged{},
		ItemRemovedFromCart{},
		CartCleared{},
		CartCheckedOut{},
	}
}
```

### Define Cart Errors

Create `internal/domain/cart/errors.go`:

```go
package cart

import "errors"

var (
	ErrCartAlreadyExists = errors.New("cart already exists")
	ErrCartNotFound      = errors.New("cart not found")
	ErrCartEmpty         = errors.New("cart is empty")
	ErrCartCheckedOut    = errors.New("cart already checked out")
	ErrItemNotInCart     = errors.New("item not in cart")
	ErrInvalidQuantity   = errors.New("quantity must be positive")
	ErrInvalidProduct    = errors.New("product ID cannot be empty")
)
```

### Build the Cart Aggregate

Create `internal/domain/cart/aggregate.go`:

```go
package cart

import (
	"fmt"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
)

// Item represents an item in the shopping cart.
type Item struct {
	ProductID   string
	ProductName string
	Quantity    int
	UnitPrice   float64
}

// Subtotal returns the item's total price.
func (i *Item) Subtotal() float64 {
	return float64(i.Quantity) * i.UnitPrice
}

// Cart is the aggregate root for shopping cart management.
type Cart struct {
	mink.AggregateBase

	CustomerID   string
	Items        map[string]*Item // ProductID -> Item
	IsCheckedOut bool
	CheckoutOrderID string
	CreatedAt    time.Time
}

// NewCart creates a new Cart aggregate.
func NewCart(id string) *Cart {
	c := &Cart{
		Items: make(map[string]*Item),
	}
	c.SetID(id)
	c.SetType("Cart")
	return c
}

// StreamID returns the event stream ID for this cart.
func (c *Cart) StreamID() string {
	return fmt.Sprintf("Cart-%s", c.AggregateID())
}

// --- Commands ---

// Create initializes a new shopping cart.
func (c *Cart) Create(customerID string) error {
	if c.Version() > 0 {
		return ErrCartAlreadyExists
	}

	c.Apply(CartCreated{
		CartID:     c.AggregateID(),
		CustomerID: customerID,
		CreatedAt:  time.Now(),
	})

	c.CustomerID = customerID
	c.CreatedAt = time.Now()
	return nil
}

// AddItem adds a product to the cart or increases quantity if already present.
func (c *Cart) AddItem(productID, productName string, quantity int, unitPrice float64) error {
	if c.Version() == 0 {
		return ErrCartNotFound
	}
	if c.IsCheckedOut {
		return ErrCartCheckedOut
	}
	if productID == "" {
		return ErrInvalidProduct
	}
	if quantity <= 0 {
		return ErrInvalidQuantity
	}

	// Check if item already exists
	if existing, ok := c.Items[productID]; ok {
		// Update quantity instead
		return c.ChangeItemQuantity(productID, existing.Quantity+quantity)
	}

	c.Apply(ItemAddedToCart{
		CartID:      c.AggregateID(),
		ProductID:   productID,
		ProductName: productName,
		Quantity:    quantity,
		UnitPrice:   unitPrice,
		AddedAt:     time.Now(),
	})

	c.Items[productID] = &Item{
		ProductID:   productID,
		ProductName: productName,
		Quantity:    quantity,
		UnitPrice:   unitPrice,
	}

	return nil
}

// ChangeItemQuantity updates the quantity of an item in the cart.
func (c *Cart) ChangeItemQuantity(productID string, newQuantity int) error {
	if c.Version() == 0 {
		return ErrCartNotFound
	}
	if c.IsCheckedOut {
		return ErrCartCheckedOut
	}

	item, ok := c.Items[productID]
	if !ok {
		return ErrItemNotInCart
	}

	if newQuantity <= 0 {
		// Remove item if quantity is 0 or less
		return c.RemoveItem(productID)
	}

	if newQuantity == item.Quantity {
		return nil // No change
	}

	oldQuantity := item.Quantity
	c.Apply(ItemQuantityChanged{
		CartID:      c.AggregateID(),
		ProductID:   productID,
		OldQuantity: oldQuantity,
		NewQuantity: newQuantity,
		ChangedAt:   time.Now(),
	})

	item.Quantity = newQuantity
	return nil
}

// RemoveItem removes a product from the cart.
func (c *Cart) RemoveItem(productID string) error {
	if c.Version() == 0 {
		return ErrCartNotFound
	}
	if c.IsCheckedOut {
		return ErrCartCheckedOut
	}

	if _, ok := c.Items[productID]; !ok {
		return ErrItemNotInCart
	}

	c.Apply(ItemRemovedFromCart{
		CartID:    c.AggregateID(),
		ProductID: productID,
		RemovedAt: time.Now(),
	})

	delete(c.Items, productID)
	return nil
}

// Clear removes all items from the cart.
func (c *Cart) Clear(reason string) error {
	if c.Version() == 0 {
		return ErrCartNotFound
	}
	if c.IsCheckedOut {
		return ErrCartCheckedOut
	}
	if len(c.Items) == 0 {
		return nil // Already empty
	}

	c.Apply(CartCleared{
		CartID:    c.AggregateID(),
		Reason:    reason,
		ClearedAt: time.Now(),
	})

	c.Items = make(map[string]*Item)
	return nil
}

// Checkout converts the cart to an order.
func (c *Cart) Checkout(orderID string) error {
	if c.Version() == 0 {
		return ErrCartNotFound
	}
	if c.IsCheckedOut {
		return ErrCartCheckedOut
	}
	if len(c.Items) == 0 {
		return ErrCartEmpty
	}

	// Convert items to slice for the event
	items := make([]CartItem, 0, len(c.Items))
	for _, item := range c.Items {
		items = append(items, CartItem{
			ProductID:   item.ProductID,
			ProductName: item.ProductName,
			Quantity:    item.Quantity,
			UnitPrice:   item.UnitPrice,
		})
	}

	c.Apply(CartCheckedOut{
		CartID:       c.AggregateID(),
		OrderID:      orderID,
		Items:        items,
		TotalAmount:  c.TotalAmount(),
		CheckedOutAt: time.Now(),
	})

	c.IsCheckedOut = true
	c.CheckoutOrderID = orderID
	return nil
}

// --- Event Application ---

func (c *Cart) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case CartCreated:
		c.CustomerID = e.CustomerID
		c.CreatedAt = e.CreatedAt

	case ItemAddedToCart:
		if c.Items == nil {
			c.Items = make(map[string]*Item)
		}
		c.Items[e.ProductID] = &Item{
			ProductID:   e.ProductID,
			ProductName: e.ProductName,
			Quantity:    e.Quantity,
			UnitPrice:   e.UnitPrice,
		}

	case ItemQuantityChanged:
		if item, ok := c.Items[e.ProductID]; ok {
			item.Quantity = e.NewQuantity
		}

	case ItemRemovedFromCart:
		delete(c.Items, e.ProductID)

	case CartCleared:
		c.Items = make(map[string]*Item)

	case CartCheckedOut:
		c.IsCheckedOut = true
		c.CheckoutOrderID = e.OrderID

	default:
		return fmt.Errorf("unknown event type: %T", event)
	}

	c.IncrementVersion()
	return nil
}

// --- Query Methods ---

// ItemCount returns the total number of items in the cart.
func (c *Cart) ItemCount() int {
	count := 0
	for _, item := range c.Items {
		count += item.Quantity
	}
	return count
}

// TotalAmount returns the total cart value.
func (c *Cart) TotalAmount() float64 {
	total := 0.0
	for _, item := range c.Items {
		total += item.Subtotal()
	}
	return total
}

// IsEmpty returns true if the cart has no items.
func (c *Cart) IsEmpty() bool {
	return len(c.Items) == 0
}

// GetItem returns an item by product ID.
func (c *Cart) GetItem(productID string) (*Item, bool) {
	item, ok := c.Items[productID]
	return item, ok
}
```

---

## Part 3: Order Aggregate

Finally, let's build the order aggregate with payment and fulfillment.

### Define Order Events

Create `internal/domain/order/events.go`:

```go
package order

import (
	"time"
)

// OrderLineItem represents a line item in an order.
type OrderLineItem struct {
	ProductID   string  `json:"productId"`
	ProductName string  `json:"productName"`
	Quantity    int     `json:"quantity"`
	UnitPrice   float64 `json:"unitPrice"`
	Subtotal    float64 `json:"subtotal"`
}

// ShippingAddress represents where the order should be delivered.
type ShippingAddress struct {
	Name       string `json:"name"`
	Street     string `json:"street"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postalCode"`
	Country    string `json:"country"`
}

// OrderPlaced is emitted when a new order is created from a cart.
type OrderPlaced struct {
	OrderID         string          `json:"orderId"`
	CustomerID      string          `json:"customerId"`
	CartID          string          `json:"cartId"`
	Items           []OrderLineItem `json:"items"`
	ShippingAddress ShippingAddress `json:"shippingAddress"`
	Subtotal        float64         `json:"subtotal"`
	ShippingCost    float64         `json:"shippingCost"`
	Tax             float64         `json:"tax"`
	TotalAmount     float64         `json:"totalAmount"`
	PlacedAt        time.Time       `json:"placedAt"`
}

// PaymentReceived is emitted when payment is successful.
type PaymentReceived struct {
	OrderID       string    `json:"orderId"`
	PaymentID     string    `json:"paymentId"`
	Amount        float64   `json:"amount"`
	PaymentMethod string    `json:"paymentMethod"` // "credit_card", "paypal"
	ReceivedAt    time.Time `json:"receivedAt"`
}

// PaymentFailed is emitted when payment fails.
type PaymentFailed struct {
	OrderID   string    `json:"orderId"`
	PaymentID string    `json:"paymentId"`
	Reason    string    `json:"reason"`
	FailedAt  time.Time `json:"failedAt"`
}

// OrderConfirmed is emitted when order is ready for fulfillment.
type OrderConfirmed struct {
	OrderID     string    `json:"orderId"`
	ConfirmedAt time.Time `json:"confirmedAt"`
}

// OrderShipped is emitted when the order is shipped.
type OrderShipped struct {
	OrderID        string    `json:"orderId"`
	TrackingNumber string    `json:"trackingNumber"`
	Carrier        string    `json:"carrier"`
	ShippedAt      time.Time `json:"shippedAt"`
}

// OrderDelivered is emitted when the order is delivered.
type OrderDelivered struct {
	OrderID     string    `json:"orderId"`
	DeliveredAt time.Time `json:"deliveredAt"`
	SignedBy    string    `json:"signedBy,omitempty"`
}

// OrderCancelled is emitted when an order is cancelled.
type OrderCancelled struct {
	OrderID     string    `json:"orderId"`
	Reason      string    `json:"reason"`
	CancelledBy string    `json:"cancelledBy"` // "customer", "system", "admin"
	CancelledAt time.Time `json:"cancelledAt"`
}

// RefundIssued is emitted when a refund is processed.
type RefundIssued struct {
	OrderID   string    `json:"orderId"`
	RefundID  string    `json:"refundId"`
	Amount    float64   `json:"amount"`
	Reason    string    `json:"reason"`
	IssuedAt  time.Time `json:"issuedAt"`
}

// AllEvents returns all order event types for registration.
func AllEvents() []interface{} {
	return []interface{}{
		OrderPlaced{},
		PaymentReceived{},
		PaymentFailed{},
		OrderConfirmed{},
		OrderShipped{},
		OrderDelivered{},
		OrderCancelled{},
		RefundIssued{},
	}
}
```

### Define Order Errors

Create `internal/domain/order/errors.go`:

```go
package order

import "errors"

var (
	ErrOrderAlreadyExists    = errors.New("order already exists")
	ErrOrderNotFound         = errors.New("order not found")
	ErrOrderAlreadyCancelled = errors.New("order is cancelled")
	ErrOrderAlreadyShipped   = errors.New("order already shipped")
	ErrOrderNotPaid          = errors.New("order not paid")
	ErrOrderNotConfirmed     = errors.New("order not confirmed")
	ErrInvalidPayment        = errors.New("invalid payment amount")
	ErrEmptyOrder            = errors.New("order has no items")
	ErrInvalidAddress        = errors.New("shipping address is required")
)
```

### Build the Order Aggregate

Create `internal/domain/order/aggregate.go`:

```go
package order

import (
	"fmt"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
)

// OrderStatus represents the current status of an order.
type OrderStatus string

const (
	StatusPending    OrderStatus = "pending"
	StatusPaid       OrderStatus = "paid"
	StatusConfirmed  OrderStatus = "confirmed"
	StatusShipped    OrderStatus = "shipped"
	StatusDelivered  OrderStatus = "delivered"
	StatusCancelled  OrderStatus = "cancelled"
)

// Order is the aggregate root for order management.
type Order struct {
	mink.AggregateBase

	// Order details
	CustomerID      string
	CartID          string
	Items           []OrderLineItem
	ShippingAddress ShippingAddress

	// Pricing
	Subtotal     float64
	ShippingCost float64
	Tax          float64
	TotalAmount  float64

	// Status tracking
	Status         OrderStatus
	PaymentID      string
	TrackingNumber string
	Carrier        string

	// Timestamps
	PlacedAt     time.Time
	PaidAt       *time.Time
	ConfirmedAt  *time.Time
	ShippedAt    *time.Time
	DeliveredAt  *time.Time
	CancelledAt  *time.Time
}

// NewOrder creates a new Order aggregate.
func NewOrder(id string) *Order {
	o := &Order{
		Status: StatusPending,
	}
	o.SetID(id)
	o.SetType("Order")
	return o
}

// StreamID returns the event stream ID for this order.
func (o *Order) StreamID() string {
	return fmt.Sprintf("Order-%s", o.AggregateID())
}

// --- Commands ---

// Place creates a new order from cart data.
func (o *Order) Place(customerID, cartID string, items []OrderLineItem, 
	address ShippingAddress, shippingCost, taxRate float64) error {
	
	if o.Version() > 0 {
		return ErrOrderAlreadyExists
	}
	if len(items) == 0 {
		return ErrEmptyOrder
	}
	if address.Street == "" || address.City == "" {
		return ErrInvalidAddress
	}

	// Calculate totals
	subtotal := 0.0
	for _, item := range items {
		subtotal += item.Subtotal
	}
	tax := subtotal * taxRate
	total := subtotal + shippingCost + tax

	o.Apply(OrderPlaced{
		OrderID:         o.AggregateID(),
		CustomerID:      customerID,
		CartID:          cartID,
		Items:           items,
		ShippingAddress: address,
		Subtotal:        subtotal,
		ShippingCost:    shippingCost,
		Tax:             tax,
		TotalAmount:     total,
		PlacedAt:        time.Now(),
	})

	// Update state
	o.CustomerID = customerID
	o.CartID = cartID
	o.Items = items
	o.ShippingAddress = address
	o.Subtotal = subtotal
	o.ShippingCost = shippingCost
	o.Tax = tax
	o.TotalAmount = total
	o.Status = StatusPending
	o.PlacedAt = time.Now()

	return nil
}

// ReceivePayment records a successful payment.
func (o *Order) ReceivePayment(paymentID string, amount float64, method string) error {
	if o.Version() == 0 {
		return ErrOrderNotFound
	}
	if o.Status == StatusCancelled {
		return ErrOrderAlreadyCancelled
	}
	if amount < o.TotalAmount {
		return ErrInvalidPayment
	}

	now := time.Now()
	o.Apply(PaymentReceived{
		OrderID:       o.AggregateID(),
		PaymentID:     paymentID,
		Amount:        amount,
		PaymentMethod: method,
		ReceivedAt:    now,
	})

	o.PaymentID = paymentID
	o.Status = StatusPaid
	o.PaidAt = &now
	return nil
}

// RecordPaymentFailure records a failed payment attempt.
func (o *Order) RecordPaymentFailure(paymentID, reason string) error {
	if o.Version() == 0 {
		return ErrOrderNotFound
	}

	o.Apply(PaymentFailed{
		OrderID:   o.AggregateID(),
		PaymentID: paymentID,
		Reason:    reason,
		FailedAt:  time.Now(),
	})

	// Status remains pending
	return nil
}

// Confirm marks the order as ready for fulfillment.
func (o *Order) Confirm() error {
	if o.Version() == 0 {
		return ErrOrderNotFound
	}
	if o.Status == StatusCancelled {
		return ErrOrderAlreadyCancelled
	}
	if o.Status != StatusPaid {
		return ErrOrderNotPaid
	}

	now := time.Now()
	o.Apply(OrderConfirmed{
		OrderID:     o.AggregateID(),
		ConfirmedAt: now,
	})

	o.Status = StatusConfirmed
	o.ConfirmedAt = &now
	return nil
}

// Ship marks the order as shipped with tracking info.
func (o *Order) Ship(trackingNumber, carrier string) error {
	if o.Version() == 0 {
		return ErrOrderNotFound
	}
	if o.Status == StatusCancelled {
		return ErrOrderAlreadyCancelled
	}
	if o.Status != StatusConfirmed {
		return ErrOrderNotConfirmed
	}

	now := time.Now()
	o.Apply(OrderShipped{
		OrderID:        o.AggregateID(),
		TrackingNumber: trackingNumber,
		Carrier:        carrier,
		ShippedAt:      now,
	})

	o.TrackingNumber = trackingNumber
	o.Carrier = carrier
	o.Status = StatusShipped
	o.ShippedAt = &now
	return nil
}

// Deliver marks the order as delivered.
func (o *Order) Deliver(signedBy string) error {
	if o.Version() == 0 {
		return ErrOrderNotFound
	}
	if o.Status != StatusShipped {
		return ErrOrderAlreadyShipped
	}

	now := time.Now()
	o.Apply(OrderDelivered{
		OrderID:     o.AggregateID(),
		DeliveredAt: now,
		SignedBy:    signedBy,
	})

	o.Status = StatusDelivered
	o.DeliveredAt = &now
	return nil
}

// Cancel cancels the order.
func (o *Order) Cancel(reason, cancelledBy string) error {
	if o.Version() == 0 {
		return ErrOrderNotFound
	}
	if o.Status == StatusCancelled {
		return nil // Already cancelled
	}
	if o.Status == StatusShipped || o.Status == StatusDelivered {
		return ErrOrderAlreadyShipped
	}

	now := time.Now()
	o.Apply(OrderCancelled{
		OrderID:     o.AggregateID(),
		Reason:      reason,
		CancelledBy: cancelledBy,
		CancelledAt: now,
	})

	o.Status = StatusCancelled
	o.CancelledAt = &now
	return nil
}

// --- Event Application ---

func (o *Order) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case OrderPlaced:
		o.CustomerID = e.CustomerID
		o.CartID = e.CartID
		o.Items = e.Items
		o.ShippingAddress = e.ShippingAddress
		o.Subtotal = e.Subtotal
		o.ShippingCost = e.ShippingCost
		o.Tax = e.Tax
		o.TotalAmount = e.TotalAmount
		o.Status = StatusPending
		o.PlacedAt = e.PlacedAt

	case PaymentReceived:
		o.PaymentID = e.PaymentID
		o.Status = StatusPaid
		t := e.ReceivedAt
		o.PaidAt = &t

	case PaymentFailed:
		// Status remains pending

	case OrderConfirmed:
		o.Status = StatusConfirmed
		t := e.ConfirmedAt
		o.ConfirmedAt = &t

	case OrderShipped:
		o.TrackingNumber = e.TrackingNumber
		o.Carrier = e.Carrier
		o.Status = StatusShipped
		t := e.ShippedAt
		o.ShippedAt = &t

	case OrderDelivered:
		o.Status = StatusDelivered
		t := e.DeliveredAt
		o.DeliveredAt = &t

	case OrderCancelled:
		o.Status = StatusCancelled
		t := e.CancelledAt
		o.CancelledAt = &t

	case RefundIssued:
		// Track refund if needed

	default:
		return fmt.Errorf("unknown event type: %T", event)
	}

	o.IncrementVersion()
	return nil
}

// --- Query Methods ---

// IsPaid returns true if payment has been received.
func (o *Order) IsPaid() bool {
	return o.Status == StatusPaid || o.Status == StatusConfirmed || 
		o.Status == StatusShipped || o.Status == StatusDelivered
}

// CanBeCancelled returns true if the order can still be cancelled.
func (o *Order) CanBeCancelled() bool {
	return o.Status == StatusPending || o.Status == StatusPaid || o.Status == StatusConfirmed
}

// ItemCount returns the total number of items.
func (o *Order) ItemCount() int {
	count := 0
	for _, item := range o.Items {
		count += item.Quantity
	}
	return count
}
```

---

## Step 4: Register All Events

Update your main application to register all event types.

Update `cmd/server/main.go` (add after creating the store):

```go
import (
	// ... existing imports
	"minkshop/internal/domain/product"
	"minkshop/internal/domain/cart"
	"minkshop/internal/domain/order"
)

func run(ctx context.Context) error {
	// ... existing code ...

	// Create the event store
	store := mink.New(adapter)

	// Register all domain events
	fmt.Println("📝 Registering domain events...")
	store.RegisterEvents(product.AllEvents()...)
	store.RegisterEvents(cart.AllEvents()...)
	store.RegisterEvents(order.AllEvents()...)

	// ... rest of the code
}
```

---

## Step 5: Test the Aggregates

Let's write tests for our domain models.

Create `tests/product_test.go`:

```go
package tests

import (
	"errors"
	"testing"

	"minkshop/internal/domain/product"

	"github.com/AshkanYarmoradi/go-mink/testing/bdd"
)

func TestProduct_Create(t *testing.T) {
	t.Run("can create a new product", func(t *testing.T) {
		p := product.NewProduct("PROD-001")

		bdd.Given(t, p).
			When(func() error {
				return p.Create("SKU123", "Widget", "A fine widget", 29.99, 100)
			}).
			Then(product.ProductCreated{
				ProductID:    "PROD-001",
				SKU:          "SKU123",
				Name:         "Widget",
				Description:  "A fine widget",
				Price:        29.99,
				InitialStock: 100,
			})
	})

	t.Run("cannot create product twice", func(t *testing.T) {
		p := product.NewProduct("PROD-001")

		bdd.Given(t, p,
			product.ProductCreated{ProductID: "PROD-001", SKU: "SKU123"},
		).
			When(func() error {
				return p.Create("SKU456", "Another", "", 10.00, 50)
			}).
			ThenError(product.ErrProductAlreadyExists)
	})

	t.Run("rejects invalid price", func(t *testing.T) {
		p := product.NewProduct("PROD-001")

		err := p.Create("SKU123", "Widget", "", -5.00, 100)
		
		if !errors.Is(err, product.ErrInvalidPrice) {
			t.Errorf("Expected ErrInvalidPrice, got %v", err)
		}
	})
}

func TestProduct_ReserveStock(t *testing.T) {
	t.Run("can reserve available stock", func(t *testing.T) {
		p := product.NewProduct("PROD-001")
		p.ApplyEvent(product.ProductCreated{
			ProductID:    "PROD-001",
			SKU:          "SKU123",
			InitialStock: 100,
		})
		p.ClearUncommittedEvents()

		err := p.ReserveStock("ORDER-001", 10)

		if err != nil {
			t.Errorf("Expected success, got %v", err)
		}
		if p.AvailableStock != 90 {
			t.Errorf("Expected available stock 90, got %d", p.AvailableStock)
		}
		if p.ReservedStock != 10 {
			t.Errorf("Expected reserved stock 10, got %d", p.ReservedStock)
		}
	})

	t.Run("cannot reserve more than available", func(t *testing.T) {
		p := product.NewProduct("PROD-001")
		p.ApplyEvent(product.ProductCreated{
			ProductID:    "PROD-001",
			InitialStock: 5,
		})
		p.ClearUncommittedEvents()

		err := p.ReserveStock("ORDER-001", 10)

		if !errors.Is(err, product.ErrInsufficientStock) {
			t.Errorf("Expected ErrInsufficientStock, got %v", err)
		}
	})
}
```

Create `tests/cart_test.go`:

```go
package tests

import (
	"testing"

	"minkshop/internal/domain/cart"

	"github.com/AshkanYarmoradi/go-mink/testing/bdd"
)

func TestCart_AddItem(t *testing.T) {
	t.Run("can add item to cart", func(t *testing.T) {
		c := cart.NewCart("CART-001")

		bdd.Given(t, c,
			cart.CartCreated{CartID: "CART-001", CustomerID: "CUST-001"},
		).
			When(func() error {
				return c.AddItem("PROD-001", "Widget", 2, 29.99)
			}).
			Then(cart.ItemAddedToCart{
				CartID:      "CART-001",
				ProductID:   "PROD-001",
				ProductName: "Widget",
				Quantity:    2,
				UnitPrice:   29.99,
			})
	})

	t.Run("adding same item increases quantity", func(t *testing.T) {
		c := cart.NewCart("CART-001")
		c.ApplyEvent(cart.CartCreated{CartID: "CART-001", CustomerID: "CUST-001"})
		c.ApplyEvent(cart.ItemAddedToCart{
			CartID:    "CART-001",
			ProductID: "PROD-001",
			Quantity:  2,
			UnitPrice: 29.99,
		})
		c.ClearUncommittedEvents()

		err := c.AddItem("PROD-001", "Widget", 3, 29.99)

		if err != nil {
			t.Errorf("Expected success, got %v", err)
		}
		// Should emit ItemQuantityChanged, not ItemAddedToCart
		events := c.UncommittedEvents()
		if len(events) != 1 {
			t.Fatalf("Expected 1 event, got %d", len(events))
		}
		if _, ok := events[0].(cart.ItemQuantityChanged); !ok {
			t.Errorf("Expected ItemQuantityChanged, got %T", events[0])
		}
	})
}

func TestCart_Checkout(t *testing.T) {
	t.Run("can checkout cart with items", func(t *testing.T) {
		c := cart.NewCart("CART-001")
		c.ApplyEvent(cart.CartCreated{CartID: "CART-001", CustomerID: "CUST-001"})
		c.ApplyEvent(cart.ItemAddedToCart{
			CartID:    "CART-001",
			ProductID: "PROD-001",
			Quantity:  2,
			UnitPrice: 29.99,
		})
		c.ClearUncommittedEvents()

		err := c.Checkout("ORDER-001")

		if err != nil {
			t.Errorf("Expected success, got %v", err)
		}
		if !c.IsCheckedOut {
			t.Error("Expected cart to be checked out")
		}
	})

	t.Run("cannot checkout empty cart", func(t *testing.T) {
		c := cart.NewCart("CART-001")
		c.ApplyEvent(cart.CartCreated{CartID: "CART-001", CustomerID: "CUST-001"})
		c.ClearUncommittedEvents()

		err := c.Checkout("ORDER-001")

		if err != cart.ErrCartEmpty {
			t.Errorf("Expected ErrCartEmpty, got %v", err)
		}
	})
}
```

Run the tests:

```bash
go test -v ./tests/...
```

---

## What's Next?

You've built a complete domain model with:

- ✅ Product aggregate with inventory management
- ✅ Shopping cart with add/remove/checkout
- ✅ Order processing with payment and fulfillment
- ✅ Domain events capturing all state changes
- ✅ Business rules enforced in aggregates

In **Part 3: Commands & CQRS**, you'll:

- Create command objects for each operation
- Build a command bus with middleware
- Add validation, logging, and idempotency
- Connect commands to aggregates via handlers

<div class="code-example" markdown="1">

**Next**: [Part 3: Commands & CQRS →](/tutorial/03-commands-cqrs)

</div>

---

## Key Takeaways

| Concept | Implementation |
|---------|----------------|
| **Events** | Immutable facts describing what happened |
| **Aggregate** | Entity that enforces business rules and produces events |
| **ApplyEvent** | Reconstructs state from historical events |
| **Stream** | All events for one aggregate instance |
| **Validation** | Performed before applying events |

---

{: .highlight }
> 💡 **Best Practice**: Events should be named in past tense (`OrderPlaced`, not `PlaceOrder`) because they describe something that already happened.
