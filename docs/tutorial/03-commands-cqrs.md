---
layout: default
title: "Part 3: Commands & CQRS"
parent: "Tutorial: Building an E-Commerce App"
nav_order: 3
permalink: /tutorial/03-commands-cqrs
---

# Part 3: Commands & CQRS
{: .no_toc }

Build a command bus with middleware for clean separation of concerns.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

In this part, you'll:

- Understand CQRS (Command Query Responsibility Segregation)
- Create command objects that represent user intentions
- Build command handlers that execute business logic
- Set up a command bus with middleware pipeline
- Add validation, logging, and idempotency

**Time**: ~40 minutes

---

## What is CQRS?

**CQRS** separates read operations (queries) from write operations (commands):

```
┌─────────────────────────────────────────────────────────────────┐
│                         Application                              │
├────────────────────────────┬────────────────────────────────────┤
│         WRITE SIDE         │           READ SIDE                │
│                            │                                    │
│   ┌─────────────────┐      │      ┌─────────────────┐          │
│   │    Commands     │      │      │     Queries     │          │
│   │  (User Intent)  │      │      │  (Data Request) │          │
│   └────────┬────────┘      │      └────────┬────────┘          │
│            │               │               │                    │
│            ▼               │               ▼                    │
│   ┌─────────────────┐      │      ┌─────────────────┐          │
│   │  Command Bus    │      │      │  Query Service  │          │
│   │  + Middleware   │      │      │                 │          │
│   └────────┬────────┘      │      └────────┬────────┘          │
│            │               │               │                    │
│            ▼               │               ▼                    │
│   ┌─────────────────┐      │      ┌─────────────────┐          │
│   │   Aggregates    │      │      │   Read Models   │          │
│   │                 │      │      │  (Projections)  │          │
│   └────────┬────────┘      │      └────────┬────────┘          │
│            │               │               │                    │
│            ▼               │               │                    │
│   ┌─────────────────┐      │               │                    │
│   │  Event Store    │──────┼───────────────┘                    │
│   └─────────────────┘      │      (Events update read models)   │
└────────────────────────────┴────────────────────────────────────┘
```

### Benefits

1. **Optimize Independently** — Write side for consistency, read side for performance
2. **Scale Separately** — More read replicas, fewer write nodes
3. **Clear Responsibilities** — Commands change state, queries only read
4. **Better Testability** — Test business logic without database queries

---

## Part 1: Define Commands

Commands represent user intentions. They describe **what the user wants to do**, not what happened.

### Product Commands

Create `internal/domain/product/commands.go`:

```go
package product

import (
	"github.com/AshkanYarmoradi/go-mink"
)

// CreateProductCommand creates a new product in the catalog.
type CreateProductCommand struct {
	mink.CommandBase
	ProductID    string  `json:"productId"`
	SKU          string  `json:"sku"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	Price        float64 `json:"price"`
	InitialStock int     `json:"initialStock"`
}

func (c CreateProductCommand) CommandType() string { return "CreateProduct" }

func (c CreateProductCommand) Validate() error {
	errs := mink.NewMultiValidationError("CreateProduct")
	
	if c.ProductID == "" {
		errs.AddField("ProductID", "product ID is required")
	}
	if c.SKU == "" {
		errs.AddField("SKU", "SKU is required")
	}
	if c.Name == "" {
		errs.AddField("Name", "name is required")
	}
	if c.Price <= 0 {
		errs.AddField("Price", "price must be positive")
	}
	if c.InitialStock < 0 {
		errs.AddField("InitialStock", "initial stock cannot be negative")
	}
	
	if errs.HasErrors() {
		return errs
	}
	return nil
}

// ChangePriceCommand updates a product's price.
type ChangePriceCommand struct {
	mink.CommandBase
	ProductID string  `json:"productId"`
	NewPrice  float64 `json:"newPrice"`
	Reason    string  `json:"reason"`
}

func (c ChangePriceCommand) CommandType() string   { return "ChangePrice" }
func (c ChangePriceCommand) AggregateID() string   { return c.ProductID }
func (c ChangePriceCommand) AggregateType() string { return "Product" }

func (c ChangePriceCommand) Validate() error {
	errs := mink.NewMultiValidationError("ChangePrice")
	
	if c.ProductID == "" {
		errs.AddField("ProductID", "product ID is required")
	}
	if c.NewPrice <= 0 {
		errs.AddField("NewPrice", "price must be positive")
	}
	
	if errs.HasErrors() {
		return errs
	}
	return nil
}

// AddStockCommand adds inventory to a product.
type AddStockCommand struct {
	mink.CommandBase
	ProductID string `json:"productId"`
	Quantity  int    `json:"quantity"`
	Reference string `json:"reference"` // PO number, etc.
}

func (c AddStockCommand) CommandType() string   { return "AddStock" }
func (c AddStockCommand) AggregateID() string   { return c.ProductID }
func (c AddStockCommand) AggregateType() string { return "Product" }

func (c AddStockCommand) Validate() error {
	errs := mink.NewMultiValidationError("AddStock")
	
	if c.ProductID == "" {
		errs.AddField("ProductID", "product ID is required")
	}
	if c.Quantity <= 0 {
		errs.AddField("Quantity", "quantity must be positive")
	}
	
	if errs.HasErrors() {
		return errs
	}
	return nil
}

// ReserveStockCommand reserves inventory for an order.
type ReserveStockCommand struct {
	mink.CommandBase
	ProductID string `json:"productId"`
	OrderID   string `json:"orderId"`
	Quantity  int    `json:"quantity"`
}

func (c ReserveStockCommand) CommandType() string   { return "ReserveStock" }
func (c ReserveStockCommand) AggregateID() string   { return c.ProductID }
func (c ReserveStockCommand) AggregateType() string { return "Product" }

func (c ReserveStockCommand) Validate() error {
	errs := mink.NewMultiValidationError("ReserveStock")
	
	if c.ProductID == "" {
		errs.AddField("ProductID", "product ID is required")
	}
	if c.OrderID == "" {
		errs.AddField("OrderID", "order ID is required")
	}
	if c.Quantity <= 0 {
		errs.AddField("Quantity", "quantity must be positive")
	}
	
	if errs.HasErrors() {
		return errs
	}
	return nil
}
```

### Cart Commands

Create `internal/domain/cart/commands.go`:

```go
package cart

import (
	"github.com/AshkanYarmoradi/go-mink"
)

// CreateCartCommand creates a new shopping cart.
type CreateCartCommand struct {
	mink.CommandBase
	CartID     string `json:"cartId"`
	CustomerID string `json:"customerId"`
}

func (c CreateCartCommand) CommandType() string { return "CreateCart" }

func (c CreateCartCommand) Validate() error {
	errs := mink.NewMultiValidationError("CreateCart")
	
	if c.CartID == "" {
		errs.AddField("CartID", "cart ID is required")
	}
	if c.CustomerID == "" {
		errs.AddField("CustomerID", "customer ID is required")
	}
	
	if errs.HasErrors() {
		return errs
	}
	return nil
}

// AddToCartCommand adds an item to the cart.
type AddToCartCommand struct {
	mink.CommandBase
	CartID      string  `json:"cartId"`
	ProductID   string  `json:"productId"`
	ProductName string  `json:"productName"`
	Quantity    int     `json:"quantity"`
	UnitPrice   float64 `json:"unitPrice"`
}

func (c AddToCartCommand) CommandType() string   { return "AddToCart" }
func (c AddToCartCommand) AggregateID() string   { return c.CartID }
func (c AddToCartCommand) AggregateType() string { return "Cart" }

func (c AddToCartCommand) Validate() error {
	errs := mink.NewMultiValidationError("AddToCart")
	
	if c.CartID == "" {
		errs.AddField("CartID", "cart ID is required")
	}
	if c.ProductID == "" {
		errs.AddField("ProductID", "product ID is required")
	}
	if c.Quantity <= 0 {
		errs.AddField("Quantity", "quantity must be positive")
	}
	if c.UnitPrice < 0 {
		errs.AddField("UnitPrice", "price cannot be negative")
	}
	
	if errs.HasErrors() {
		return errs
	}
	return nil
}

// RemoveFromCartCommand removes an item from the cart.
type RemoveFromCartCommand struct {
	mink.CommandBase
	CartID    string `json:"cartId"`
	ProductID string `json:"productId"`
}

func (c RemoveFromCartCommand) CommandType() string   { return "RemoveFromCart" }
func (c RemoveFromCartCommand) AggregateID() string   { return c.CartID }
func (c RemoveFromCartCommand) AggregateType() string { return "Cart" }

func (c RemoveFromCartCommand) Validate() error {
	errs := mink.NewMultiValidationError("RemoveFromCart")
	
	if c.CartID == "" {
		errs.AddField("CartID", "cart ID is required")
	}
	if c.ProductID == "" {
		errs.AddField("ProductID", "product ID is required")
	}
	
	if errs.HasErrors() {
		return errs
	}
	return nil
}

// CheckoutCartCommand converts the cart to an order.
type CheckoutCartCommand struct {
	mink.CommandBase
	CartID  string `json:"cartId"`
	OrderID string `json:"orderId"`
}

func (c CheckoutCartCommand) CommandType() string   { return "CheckoutCart" }
func (c CheckoutCartCommand) AggregateID() string   { return c.CartID }
func (c CheckoutCartCommand) AggregateType() string { return "Cart" }

func (c CheckoutCartCommand) Validate() error {
	errs := mink.NewMultiValidationError("CheckoutCart")
	
	if c.CartID == "" {
		errs.AddField("CartID", "cart ID is required")
	}
	if c.OrderID == "" {
		errs.AddField("OrderID", "order ID is required")
	}
	
	if errs.HasErrors() {
		return errs
	}
	return nil
}
```

### Order Commands

Create `internal/domain/order/commands.go`:

```go
package order

import (
	"github.com/AshkanYarmoradi/go-mink"
)

// PlaceOrderCommand creates a new order.
type PlaceOrderCommand struct {
	mink.CommandBase
	OrderID         string          `json:"orderId"`
	CustomerID      string          `json:"customerId"`
	CartID          string          `json:"cartId"`
	Items           []OrderLineItem `json:"items"`
	ShippingAddress ShippingAddress `json:"shippingAddress"`
	ShippingCost    float64         `json:"shippingCost"`
	TaxRate         float64         `json:"taxRate"`
}

func (c PlaceOrderCommand) CommandType() string { return "PlaceOrder" }

func (c PlaceOrderCommand) Validate() error {
	errs := mink.NewMultiValidationError("PlaceOrder")
	
	if c.OrderID == "" {
		errs.AddField("OrderID", "order ID is required")
	}
	if c.CustomerID == "" {
		errs.AddField("CustomerID", "customer ID is required")
	}
	if len(c.Items) == 0 {
		errs.AddField("Items", "at least one item is required")
	}
	if c.ShippingAddress.Street == "" {
		errs.AddField("ShippingAddress.Street", "street is required")
	}
	if c.ShippingAddress.City == "" {
		errs.AddField("ShippingAddress.City", "city is required")
	}
	
	if errs.HasErrors() {
		return errs
	}
	return nil
}

// ProcessPaymentCommand records payment for an order.
type ProcessPaymentCommand struct {
	mink.CommandBase
	OrderID       string  `json:"orderId"`
	PaymentID     string  `json:"paymentId"`
	Amount        float64 `json:"amount"`
	PaymentMethod string  `json:"paymentMethod"`
}

func (c ProcessPaymentCommand) CommandType() string   { return "ProcessPayment" }
func (c ProcessPaymentCommand) AggregateID() string   { return c.OrderID }
func (c ProcessPaymentCommand) AggregateType() string { return "Order" }

func (c ProcessPaymentCommand) Validate() error {
	errs := mink.NewMultiValidationError("ProcessPayment")
	
	if c.OrderID == "" {
		errs.AddField("OrderID", "order ID is required")
	}
	if c.PaymentID == "" {
		errs.AddField("PaymentID", "payment ID is required")
	}
	if c.Amount <= 0 {
		errs.AddField("Amount", "amount must be positive")
	}
	
	if errs.HasErrors() {
		return errs
	}
	return nil
}

// ShipOrderCommand ships an order.
type ShipOrderCommand struct {
	mink.CommandBase
	OrderID        string `json:"orderId"`
	TrackingNumber string `json:"trackingNumber"`
	Carrier        string `json:"carrier"`
}

func (c ShipOrderCommand) CommandType() string   { return "ShipOrder" }
func (c ShipOrderCommand) AggregateID() string   { return c.OrderID }
func (c ShipOrderCommand) AggregateType() string { return "Order" }

func (c ShipOrderCommand) Validate() error {
	errs := mink.NewMultiValidationError("ShipOrder")
	
	if c.OrderID == "" {
		errs.AddField("OrderID", "order ID is required")
	}
	if c.TrackingNumber == "" {
		errs.AddField("TrackingNumber", "tracking number is required")
	}
	if c.Carrier == "" {
		errs.AddField("Carrier", "carrier is required")
	}
	
	if errs.HasErrors() {
		return errs
	}
	return nil
}

// CancelOrderCommand cancels an order.
type CancelOrderCommand struct {
	mink.CommandBase
	OrderID     string `json:"orderId"`
	Reason      string `json:"reason"`
	CancelledBy string `json:"cancelledBy"`
}

func (c CancelOrderCommand) CommandType() string   { return "CancelOrder" }
func (c CancelOrderCommand) AggregateID() string   { return c.OrderID }
func (c CancelOrderCommand) AggregateType() string { return "Order" }

func (c CancelOrderCommand) Validate() error {
	errs := mink.NewMultiValidationError("CancelOrder")
	
	if c.OrderID == "" {
		errs.AddField("OrderID", "order ID is required")
	}
	if c.Reason == "" {
		errs.AddField("Reason", "cancellation reason is required")
	}
	
	if errs.HasErrors() {
		return errs
	}
	return nil
}
```

---

## Part 2: Create Command Handlers

Handlers connect commands to aggregates and manage persistence.

### Product Handler

Create `internal/handlers/product_handler.go`:

```go
package handlers

import (
	"context"
	"fmt"

	"github.com/AshkanYarmoradi/go-mink"
	"minkshop/internal/domain/product"
)

// ProductHandler handles product commands.
type ProductHandler struct {
	store *mink.EventStore
}

// NewProductHandler creates a new ProductHandler.
func NewProductHandler(store *mink.EventStore) *ProductHandler {
	return &ProductHandler{store: store}
}

// HandleCreateProduct handles CreateProductCommand.
func (h *ProductHandler) HandleCreateProduct(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	c, ok := cmd.(product.CreateProductCommand)
	if !ok {
		return mink.NewErrorResult(fmt.Errorf("invalid command type")), nil
	}

	// Create new aggregate
	p := product.NewProduct(c.ProductID)

	// Execute business logic
	if err := p.Create(c.SKU, c.Name, c.Description, c.Price, c.InitialStock); err != nil {
		return mink.NewErrorResult(err), nil
	}

	// Save to event store
	if err := h.store.SaveAggregate(ctx, p); err != nil {
		return mink.NewErrorResult(err), err
	}

	return mink.NewSuccessResult(p.AggregateID(), p.Version()), nil
}

// HandleChangePrice handles ChangePriceCommand.
func (h *ProductHandler) HandleChangePrice(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	c, ok := cmd.(product.ChangePriceCommand)
	if !ok {
		return mink.NewErrorResult(fmt.Errorf("invalid command type")), nil
	}

	// Load existing aggregate
	p := product.NewProduct(c.ProductID)
	if err := h.store.LoadAggregate(ctx, p); err != nil {
		return mink.NewErrorResult(err), err
	}

	// Execute business logic
	if err := p.ChangePrice(c.NewPrice, c.Reason); err != nil {
		return mink.NewErrorResult(err), nil
	}

	// Save changes
	if err := h.store.SaveAggregate(ctx, p); err != nil {
		return mink.NewErrorResult(err), err
	}

	return mink.NewSuccessResult(p.AggregateID(), p.Version()), nil
}

// HandleAddStock handles AddStockCommand.
func (h *ProductHandler) HandleAddStock(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	c, ok := cmd.(product.AddStockCommand)
	if !ok {
		return mink.NewErrorResult(fmt.Errorf("invalid command type")), nil
	}

	p := product.NewProduct(c.ProductID)
	if err := h.store.LoadAggregate(ctx, p); err != nil {
		return mink.NewErrorResult(err), err
	}

	if err := p.AddStock(c.Quantity, c.Reference); err != nil {
		return mink.NewErrorResult(err), nil
	}

	if err := h.store.SaveAggregate(ctx, p); err != nil {
		return mink.NewErrorResult(err), err
	}

	return mink.NewSuccessResult(p.AggregateID(), p.Version()), nil
}

// HandleReserveStock handles ReserveStockCommand.
func (h *ProductHandler) HandleReserveStock(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	c, ok := cmd.(product.ReserveStockCommand)
	if !ok {
		return mink.NewErrorResult(fmt.Errorf("invalid command type")), nil
	}

	p := product.NewProduct(c.ProductID)
	if err := h.store.LoadAggregate(ctx, p); err != nil {
		return mink.NewErrorResult(err), err
	}

	if err := p.ReserveStock(c.OrderID, c.Quantity); err != nil {
		return mink.NewErrorResult(err), nil
	}

	if err := h.store.SaveAggregate(ctx, p); err != nil {
		return mink.NewErrorResult(err), err
	}

	return mink.NewSuccessResult(p.AggregateID(), p.Version()), nil
}

// RegisterHandlers registers all product command handlers with the bus.
func (h *ProductHandler) RegisterHandlers(bus *mink.CommandBus) {
	bus.RegisterFunc("CreateProduct", h.HandleCreateProduct)
	bus.RegisterFunc("ChangePrice", h.HandleChangePrice)
	bus.RegisterFunc("AddStock", h.HandleAddStock)
	bus.RegisterFunc("ReserveStock", h.HandleReserveStock)
}
```

### Cart Handler

Create `internal/handlers/cart_handler.go`:

```go
package handlers

import (
	"context"
	"fmt"

	"github.com/AshkanYarmoradi/go-mink"
	"minkshop/internal/domain/cart"
)

// CartHandler handles cart commands.
type CartHandler struct {
	store *mink.EventStore
}

// NewCartHandler creates a new CartHandler.
func NewCartHandler(store *mink.EventStore) *CartHandler {
	return &CartHandler{store: store}
}

// HandleCreateCart handles CreateCartCommand.
func (h *CartHandler) HandleCreateCart(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	c, ok := cmd.(cart.CreateCartCommand)
	if !ok {
		return mink.NewErrorResult(fmt.Errorf("invalid command type")), nil
	}

	ct := cart.NewCart(c.CartID)

	if err := ct.Create(c.CustomerID); err != nil {
		return mink.NewErrorResult(err), nil
	}

	if err := h.store.SaveAggregate(ctx, ct); err != nil {
		return mink.NewErrorResult(err), err
	}

	return mink.NewSuccessResult(ct.AggregateID(), ct.Version()), nil
}

// HandleAddToCart handles AddToCartCommand.
func (h *CartHandler) HandleAddToCart(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	c, ok := cmd.(cart.AddToCartCommand)
	if !ok {
		return mink.NewErrorResult(fmt.Errorf("invalid command type")), nil
	}

	ct := cart.NewCart(c.CartID)
	if err := h.store.LoadAggregate(ctx, ct); err != nil {
		return mink.NewErrorResult(err), err
	}

	if err := ct.AddItem(c.ProductID, c.ProductName, c.Quantity, c.UnitPrice); err != nil {
		return mink.NewErrorResult(err), nil
	}

	if err := h.store.SaveAggregate(ctx, ct); err != nil {
		return mink.NewErrorResult(err), err
	}

	return mink.NewSuccessResultWithData(ct.AggregateID(), ct.Version(), map[string]interface{}{
		"itemCount":   ct.ItemCount(),
		"totalAmount": ct.TotalAmount(),
	}), nil
}

// HandleRemoveFromCart handles RemoveFromCartCommand.
func (h *CartHandler) HandleRemoveFromCart(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	c, ok := cmd.(cart.RemoveFromCartCommand)
	if !ok {
		return mink.NewErrorResult(fmt.Errorf("invalid command type")), nil
	}

	ct := cart.NewCart(c.CartID)
	if err := h.store.LoadAggregate(ctx, ct); err != nil {
		return mink.NewErrorResult(err), err
	}

	if err := ct.RemoveItem(c.ProductID); err != nil {
		return mink.NewErrorResult(err), nil
	}

	if err := h.store.SaveAggregate(ctx, ct); err != nil {
		return mink.NewErrorResult(err), err
	}

	return mink.NewSuccessResult(ct.AggregateID(), ct.Version()), nil
}

// HandleCheckoutCart handles CheckoutCartCommand.
func (h *CartHandler) HandleCheckoutCart(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	c, ok := cmd.(cart.CheckoutCartCommand)
	if !ok {
		return mink.NewErrorResult(fmt.Errorf("invalid command type")), nil
	}

	ct := cart.NewCart(c.CartID)
	if err := h.store.LoadAggregate(ctx, ct); err != nil {
		return mink.NewErrorResult(err), err
	}

	if err := ct.Checkout(c.OrderID); err != nil {
		return mink.NewErrorResult(err), nil
	}

	if err := h.store.SaveAggregate(ctx, ct); err != nil {
		return mink.NewErrorResult(err), err
	}

	return mink.NewSuccessResultWithData(ct.AggregateID(), ct.Version(), map[string]interface{}{
		"orderID": c.OrderID,
	}), nil
}

// RegisterHandlers registers all cart command handlers with the bus.
func (h *CartHandler) RegisterHandlers(bus *mink.CommandBus) {
	bus.RegisterFunc("CreateCart", h.HandleCreateCart)
	bus.RegisterFunc("AddToCart", h.HandleAddToCart)
	bus.RegisterFunc("RemoveFromCart", h.HandleRemoveFromCart)
	bus.RegisterFunc("CheckoutCart", h.HandleCheckoutCart)
}
```

### Order Handler

Create `internal/handlers/order_handler.go`:

```go
package handlers

import (
	"context"
	"fmt"

	"github.com/AshkanYarmoradi/go-mink"
	"minkshop/internal/domain/order"
)

// OrderHandler handles order commands.
type OrderHandler struct {
	store *mink.EventStore
}

// NewOrderHandler creates a new OrderHandler.
func NewOrderHandler(store *mink.EventStore) *OrderHandler {
	return &OrderHandler{store: store}
}

// HandlePlaceOrder handles PlaceOrderCommand.
func (h *OrderHandler) HandlePlaceOrder(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	c, ok := cmd.(order.PlaceOrderCommand)
	if !ok {
		return mink.NewErrorResult(fmt.Errorf("invalid command type")), nil
	}

	o := order.NewOrder(c.OrderID)

	if err := o.Place(c.CustomerID, c.CartID, c.Items, c.ShippingAddress, c.ShippingCost, c.TaxRate); err != nil {
		return mink.NewErrorResult(err), nil
	}

	if err := h.store.SaveAggregate(ctx, o); err != nil {
		return mink.NewErrorResult(err), err
	}

	return mink.NewSuccessResultWithData(o.AggregateID(), o.Version(), map[string]interface{}{
		"totalAmount": o.TotalAmount,
		"status":      o.Status,
	}), nil
}

// HandleProcessPayment handles ProcessPaymentCommand.
func (h *OrderHandler) HandleProcessPayment(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	c, ok := cmd.(order.ProcessPaymentCommand)
	if !ok {
		return mink.NewErrorResult(fmt.Errorf("invalid command type")), nil
	}

	o := order.NewOrder(c.OrderID)
	if err := h.store.LoadAggregate(ctx, o); err != nil {
		return mink.NewErrorResult(err), err
	}

	if err := o.ReceivePayment(c.PaymentID, c.Amount, c.PaymentMethod); err != nil {
		return mink.NewErrorResult(err), nil
	}

	// Auto-confirm after payment
	if err := o.Confirm(); err != nil {
		return mink.NewErrorResult(err), nil
	}

	if err := h.store.SaveAggregate(ctx, o); err != nil {
		return mink.NewErrorResult(err), err
	}

	return mink.NewSuccessResultWithData(o.AggregateID(), o.Version(), map[string]interface{}{
		"status": o.Status,
	}), nil
}

// HandleShipOrder handles ShipOrderCommand.
func (h *OrderHandler) HandleShipOrder(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	c, ok := cmd.(order.ShipOrderCommand)
	if !ok {
		return mink.NewErrorResult(fmt.Errorf("invalid command type")), nil
	}

	o := order.NewOrder(c.OrderID)
	if err := h.store.LoadAggregate(ctx, o); err != nil {
		return mink.NewErrorResult(err), err
	}

	if err := o.Ship(c.TrackingNumber, c.Carrier); err != nil {
		return mink.NewErrorResult(err), nil
	}

	if err := h.store.SaveAggregate(ctx, o); err != nil {
		return mink.NewErrorResult(err), err
	}

	return mink.NewSuccessResultWithData(o.AggregateID(), o.Version(), map[string]interface{}{
		"trackingNumber": c.TrackingNumber,
		"carrier":        c.Carrier,
	}), nil
}

// HandleCancelOrder handles CancelOrderCommand.
func (h *OrderHandler) HandleCancelOrder(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	c, ok := cmd.(order.CancelOrderCommand)
	if !ok {
		return mink.NewErrorResult(fmt.Errorf("invalid command type")), nil
	}

	o := order.NewOrder(c.OrderID)
	if err := h.store.LoadAggregate(ctx, o); err != nil {
		return mink.NewErrorResult(err), err
	}

	if err := o.Cancel(c.Reason, c.CancelledBy); err != nil {
		return mink.NewErrorResult(err), nil
	}

	if err := h.store.SaveAggregate(ctx, o); err != nil {
		return mink.NewErrorResult(err), err
	}

	return mink.NewSuccessResult(o.AggregateID(), o.Version()), nil
}

// RegisterHandlers registers all order command handlers with the bus.
func (h *OrderHandler) RegisterHandlers(bus *mink.CommandBus) {
	bus.RegisterFunc("PlaceOrder", h.HandlePlaceOrder)
	bus.RegisterFunc("ProcessPayment", h.HandleProcessPayment)
	bus.RegisterFunc("ShipOrder", h.HandleShipOrder)
	bus.RegisterFunc("CancelOrder", h.HandleCancelOrder)
}
```

---

## Part 3: Set Up the Command Bus

Now let's wire everything together with middleware.

### Create Application Service

Create `internal/app/app.go`:

```go
package app

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/postgres"
	
	"minkshop/internal/domain/cart"
	"minkshop/internal/domain/order"
	"minkshop/internal/domain/product"
	"minkshop/internal/handlers"
)

// Application holds all application dependencies.
type Application struct {
	Store      *mink.EventStore
	CommandBus *mink.CommandBus
	adapter    *postgres.PostgresAdapter
}

// Config holds application configuration.
type Config struct {
	DatabaseURL    string
	DatabaseSchema string
	MaxConnections int
}

// New creates a new Application instance.
func New(ctx context.Context, cfg Config) (*Application, error) {
	// Create PostgreSQL adapter
	adapter, err := postgres.NewAdapter(cfg.DatabaseURL,
		postgres.WithSchema(cfg.DatabaseSchema),
		postgres.WithMaxConnections(cfg.MaxConnections),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create adapter: %w", err)
	}

	// Initialize schema
	if err := adapter.Initialize(ctx); err != nil {
		adapter.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Create event store
	store := mink.New(adapter)

	// Register all domain events
	store.RegisterEvents(product.AllEvents()...)
	store.RegisterEvents(cart.AllEvents()...)
	store.RegisterEvents(order.AllEvents()...)

	// Create command bus with middleware
	bus := mink.NewCommandBus()
	
	// Add middleware in order
	bus.Use(
		mink.RecoveryMiddleware(),      // Recover from panics
		mink.ValidationMiddleware(),     // Validate commands
		NewLoggingMiddleware(),          // Log command execution
		NewTimingMiddleware(),           // Track execution time
	)

	// Register command handlers
	productHandler := handlers.NewProductHandler(store)
	productHandler.RegisterHandlers(bus)

	cartHandler := handlers.NewCartHandler(store)
	cartHandler.RegisterHandlers(bus)

	orderHandler := handlers.NewOrderHandler(store)
	orderHandler.RegisterHandlers(bus)

	return &Application{
		Store:      store,
		CommandBus: bus,
		adapter:    adapter,
	}, nil
}

// Close releases all resources.
func (a *Application) Close() error {
	if a.adapter != nil {
		return a.adapter.Close()
	}
	return nil
}

// NewLoggingMiddleware creates a logging middleware.
func NewLoggingMiddleware() mink.Middleware {
	return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
		return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			log.Printf("[CMD] Dispatching %s", cmd.CommandType())
			
			result, err := next(ctx, cmd)
			
			if err != nil {
				log.Printf("[CMD] %s failed: %v", cmd.CommandType(), err)
			} else if result.IsError() {
				log.Printf("[CMD] %s returned error: %v", cmd.CommandType(), result.Error)
			} else {
				log.Printf("[CMD] %s succeeded (aggregate: %s, version: %d)", 
					cmd.CommandType(), result.AggregateID, result.Version)
			}
			
			return result, err
		}
	}
}

// NewTimingMiddleware creates a timing middleware.
func NewTimingMiddleware() mink.Middleware {
	return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
		return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			start := time.Now()
			
			result, err := next(ctx, cmd)
			
			duration := time.Since(start)
			if duration > 100*time.Millisecond {
				log.Printf("[PERF] %s took %v (slow!)", cmd.CommandType(), duration)
			}
			
			return result, err
		}
	}
}
```

---

## Part 4: Update Main Application

Update `cmd/server/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"minkshop/internal/app"
	"minkshop/internal/domain/cart"
	"minkshop/internal/domain/order"
	"minkshop/internal/domain/product"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		fmt.Println("\nShutting down...")
		cancel()
	}()

	if err := run(ctx); err != nil {
		log.Fatalf("Application error: %v", err)
	}
}

func run(ctx context.Context) error {
	fmt.Println("🛒 MinkShop - Event Sourced E-Commerce")
	fmt.Println("=====================================")
	fmt.Println()

	// Initialize application
	application, err := app.New(ctx, app.Config{
		DatabaseURL:    getEnvOrDefault("DATABASE_URL", "postgres://minkshop:secret@localhost:5432/minkshop?sslmode=disable"),
		DatabaseSchema: "minkshop",
		MaxConnections: 10,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize application: %w", err)
	}
	defer application.Close()

	fmt.Println("✅ Application initialized")
	fmt.Println()

	// Demo: Execute some commands
	if err := runDemo(ctx, application); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Press Ctrl+C to exit...")
	<-ctx.Done()

	fmt.Println("👋 Goodbye!")
	return nil
}

func runDemo(ctx context.Context, app *app.Application) error {
	fmt.Println("📝 Running demo commands...")
	fmt.Println()

	// 1. Create a product
	fmt.Println("1. Creating product...")
	result, err := app.CommandBus.Dispatch(ctx, product.CreateProductCommand{
		ProductID:    "PROD-001",
		SKU:          "WIDGET-001",
		Name:         "Super Widget",
		Description:  "The best widget money can buy",
		Price:        29.99,
		InitialStock: 100,
	})
	if err != nil {
		return fmt.Errorf("create product failed: %w", err)
	}
	if result.IsError() {
		return fmt.Errorf("create product error: %w", result.Error)
	}
	fmt.Printf("   ✅ Product created: %s (version %d)\n", result.AggregateID, result.Version)

	// 2. Create a cart
	fmt.Println("2. Creating cart...")
	result, err = app.CommandBus.Dispatch(ctx, cart.CreateCartCommand{
		CartID:     "CART-001",
		CustomerID: "CUST-001",
	})
	if err != nil {
		return fmt.Errorf("create cart failed: %w", err)
	}
	fmt.Printf("   ✅ Cart created: %s\n", result.AggregateID)

	// 3. Add item to cart
	fmt.Println("3. Adding item to cart...")
	result, err = app.CommandBus.Dispatch(ctx, cart.AddToCartCommand{
		CartID:      "CART-001",
		ProductID:   "PROD-001",
		ProductName: "Super Widget",
		Quantity:    2,
		UnitPrice:   29.99,
	})
	if err != nil {
		return fmt.Errorf("add to cart failed: %w", err)
	}
	if data, ok := result.Data.(map[string]interface{}); ok {
		fmt.Printf("   ✅ Item added: %d items, total $%.2f\n", 
			data["itemCount"], data["totalAmount"])
	}

	// 4. Checkout cart
	fmt.Println("4. Checking out cart...")
	result, err = app.CommandBus.Dispatch(ctx, cart.CheckoutCartCommand{
		CartID:  "CART-001",
		OrderID: "ORDER-001",
	})
	if err != nil {
		return fmt.Errorf("checkout failed: %w", err)
	}
	fmt.Printf("   ✅ Cart checked out, order: %s\n", "ORDER-001")

	// 5. Place order
	fmt.Println("5. Placing order...")
	result, err = app.CommandBus.Dispatch(ctx, order.PlaceOrderCommand{
		OrderID:    "ORDER-001",
		CustomerID: "CUST-001",
		CartID:     "CART-001",
		Items: []order.OrderLineItem{
			{ProductID: "PROD-001", ProductName: "Super Widget", Quantity: 2, UnitPrice: 29.99, Subtotal: 59.98},
		},
		ShippingAddress: order.ShippingAddress{
			Name:       "John Doe",
			Street:     "123 Main St",
			City:       "New York",
			State:      "NY",
			PostalCode: "10001",
			Country:    "USA",
		},
		ShippingCost: 5.99,
		TaxRate:      0.08,
	})
	if err != nil {
		return fmt.Errorf("place order failed: %w", err)
	}
	if data, ok := result.Data.(map[string]interface{}); ok {
		fmt.Printf("   ✅ Order placed: total $%.2f, status: %s\n", 
			data["totalAmount"], data["status"])
	}

	// 6. Process payment
	fmt.Println("6. Processing payment...")
	result, err = app.CommandBus.Dispatch(ctx, order.ProcessPaymentCommand{
		OrderID:       "ORDER-001",
		PaymentID:     "PAY-001",
		Amount:        70.76, // 59.98 + 5.99 + 4.79 tax
		PaymentMethod: "credit_card",
	})
	if err != nil {
		return fmt.Errorf("process payment failed: %w", err)
	}
	if data, ok := result.Data.(map[string]interface{}); ok {
		fmt.Printf("   ✅ Payment processed, status: %s\n", data["status"])
	}

	// 7. Ship order
	fmt.Println("7. Shipping order...")
	result, err = app.CommandBus.Dispatch(ctx, order.ShipOrderCommand{
		OrderID:        "ORDER-001",
		TrackingNumber: "1Z999AA10123456784",
		Carrier:        "UPS",
	})
	if err != nil {
		return fmt.Errorf("ship order failed: %w", err)
	}
	if data, ok := result.Data.(map[string]interface{}); ok {
		fmt.Printf("   ✅ Order shipped: %s via %s\n", 
			data["trackingNumber"], data["carrier"])
	}

	fmt.Println()
	fmt.Println("🎉 Demo completed successfully!")
	
	return nil
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
```

---

## Step 5: Run the Application

```bash
# Start PostgreSQL
docker-compose up -d

# Run the application
go run cmd/server/main.go
```

Expected output:

```
🛒 MinkShop - Event Sourced E-Commerce
=====================================

✅ Application initialized

📝 Running demo commands...

1. Creating product...
[CMD] Dispatching CreateProduct
[CMD] CreateProduct succeeded (aggregate: PROD-001, version: 1)
   ✅ Product created: PROD-001 (version 1)
2. Creating cart...
[CMD] Dispatching CreateCart
[CMD] CreateCart succeeded (aggregate: CART-001, version: 1)
   ✅ Cart created: CART-001
3. Adding item to cart...
[CMD] Dispatching AddToCart
[CMD] AddToCart succeeded (aggregate: CART-001, version: 2)
   ✅ Item added: 2 items, total $59.98
4. Checking out cart...
[CMD] Dispatching CheckoutCart
[CMD] CheckoutCart succeeded (aggregate: CART-001, version: 3)
   ✅ Cart checked out, order: ORDER-001
5. Placing order...
[CMD] Dispatching PlaceOrder
[CMD] PlaceOrder succeeded (aggregate: ORDER-001, version: 1)
   ✅ Order placed: total $70.76, status: pending
6. Processing payment...
[CMD] Dispatching ProcessPayment
[CMD] ProcessPayment succeeded (aggregate: ORDER-001, version: 3)
   ✅ Payment processed, status: confirmed
7. Shipping order...
[CMD] Dispatching ShipOrder
[CMD] ShipOrder succeeded (aggregate: ORDER-001, version: 4)
   ✅ Order shipped: 1Z999AA10123456784 via UPS

🎉 Demo completed successfully!
```

---

## What's Next?

You've implemented the command side of CQRS with:

- ✅ Command objects with validation
- ✅ Command handlers for each aggregate
- ✅ Command bus with middleware pipeline
- ✅ Logging and timing middleware
- ✅ Complete order workflow demonstration

In **Part 4: Projections & Queries**, you'll:

- Build read models for fast queries
- Create inline and async projections
- Query order history and product catalog
- Add real-time dashboard updates

<div class="code-example" markdown="1">

**Next**: [Part 4: Projections & Queries →](/tutorial/04-projections)

</div>

---

## Key Takeaways

| Concept | Description |
|---------|-------------|
| **Command** | User intention, validated before execution |
| **Handler** | Connects command to aggregate and persistence |
| **Command Bus** | Routes commands through middleware to handlers |
| **Middleware** | Cross-cutting concerns (logging, validation, timing) |
| **CQRS** | Separate write (commands) from read (queries) |

---

{: .highlight }
> 💡 **Best Practice**: Commands should be validated at the API boundary **and** in the aggregate. API validation catches format errors, aggregate validation enforces business rules.
