---
layout: default
title: "Part 5: Testing"
parent: "Tutorial: Building an E-Commerce App"
nav_order: 5
permalink: /tutorial/05-testing
---

# Part 5: Testing
{: .no_toc }

Test your event-sourced application with confidence.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

In this part, you'll:

- Write BDD-style aggregate tests
- Test projections with fixtures
- Use event assertions
- Add integration tests with containers

**Time**: ~35 minutes

---

## Why Testing Event Sourcing is Different

Event-sourced systems are actually **easier** to test because:

1. **Deterministic**: Given the same events, you always get the same state
2. **Observable**: Every state change produces a visible event
3. **Time-travel**: You can replay any historical scenario

```
Traditional Testing:         Event Sourcing Testing:
─────────────────────────────────────────────────────────
Setup DB state               Given these events happened
Call method                  When I do this action
Assert DB state changed      Then these events are produced
```

---

## Part 1: BDD-Style Aggregate Tests

The `testing/bdd` package provides a fluent API for testing aggregates.

Create `internal/domain/product/product_test.go`:

```go
package product_test

import (
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/testing/bdd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"minkshop/internal/domain/product"
)

func TestProduct_Create(t *testing.T) {
	t.Run("creates product with valid data", func(t *testing.T) {
		p := product.NewProduct("PROD-001")

		bdd.Given(t, p).
			When(func() error {
				return p.Create("WIDGET-001", "Widget Pro", "A professional widget", 99.99, 100)
			}).
			Then(product.ProductCreated{
				ProductID:    "PROD-001",
				SKU:          "WIDGET-001",
				Name:         "Widget Pro",
				Description:  "A professional widget",
				Price:        99.99,
				InitialStock: 100,
			})
	})

	t.Run("rejects empty SKU", func(t *testing.T) {
		p := product.NewProduct("PROD-001")

		bdd.Given(t, p).
			When(func() error {
				return p.Create("", "Widget Pro", "Description", 99.99, 100)
			}).
			ThenError(product.ErrInvalidSKU)
	})

	t.Run("rejects zero price", func(t *testing.T) {
		p := product.NewProduct("PROD-001")

		bdd.Given(t, p).
			When(func() error {
				return p.Create("SKU-001", "Widget", "Description", 0, 100)
			}).
			ThenError(product.ErrInvalidPrice)
	})

	t.Run("rejects negative stock", func(t *testing.T) {
		p := product.NewProduct("PROD-001")

		bdd.Given(t, p).
			When(func() error {
				return p.Create("SKU-001", "Widget", "Description", 99.99, -5)
			}).
			ThenError(product.ErrInvalidStock)
	})
}

func TestProduct_ReserveStock(t *testing.T) {
	t.Run("reserves available stock", func(t *testing.T) {
		p := product.NewProduct("PROD-001")

		bdd.Given(t, p,
			product.ProductCreated{
				ProductID:    "PROD-001",
				SKU:          "WIDGET-001",
				Name:         "Widget Pro",
				Price:        99.99,
				InitialStock: 100,
			},
		).
			When(func() error {
				return p.ReserveStock("ORDER-001", 10)
			}).
			Then(product.StockReserved{
				ProductID:    "PROD-001",
				OrderID:      "ORDER-001",
				Quantity:     10,
			})
	})

	t.Run("rejects when insufficient stock", func(t *testing.T) {
		p := product.NewProduct("PROD-001")

		bdd.Given(t, p,
			product.ProductCreated{
				ProductID:    "PROD-001",
				SKU:          "WIDGET-001",
				Name:         "Widget Pro",
				Price:        99.99,
				InitialStock: 5,
			},
		).
			When(func() error {
				return p.ReserveStock("ORDER-001", 10)
			}).
			ThenError(product.ErrInsufficientStock)
	})

	t.Run("rejects after discontinuation", func(t *testing.T) {
		p := product.NewProduct("PROD-001")

		bdd.Given(t, p,
			product.ProductCreated{
				ProductID:    "PROD-001",
				SKU:          "WIDGET-001",
				Name:         "Widget Pro",
				Price:        99.99,
				InitialStock: 100,
			},
			product.ProductDiscontinued{
				ProductID: "PROD-001",
				Reason:    "No longer manufactured",
			},
		).
			When(func() error {
				return p.ReserveStock("ORDER-001", 10)
			}).
			ThenError(product.ErrProductDiscontinued)
	})
}

func TestProduct_ReleaseStock(t *testing.T) {
	t.Run("releases previously reserved stock", func(t *testing.T) {
		p := product.NewProduct("PROD-001")

		bdd.Given(t, p,
			product.ProductCreated{
				ProductID:    "PROD-001",
				SKU:          "WIDGET-001",
				Name:         "Widget Pro",
				Price:        99.99,
				InitialStock: 100,
			},
			product.StockReserved{
				ProductID: "PROD-001",
				OrderID:   "ORDER-001",
				Quantity:  10,
			},
		).
			When(func() error {
				return p.ReleaseStock("ORDER-001", 10)
			}).
			Then(product.StockReleased{
				ProductID: "PROD-001",
				OrderID:   "ORDER-001",
				Quantity:  10,
			})
	})
}

func TestProduct_StateTransitions(t *testing.T) {
	t.Run("tracks available stock correctly", func(t *testing.T) {
		p := product.NewProduct("PROD-001")

		// Apply events directly to check state
		require.NoError(t, p.ApplyEvent(product.ProductCreated{
			ProductID:    "PROD-001",
			SKU:          "WIDGET-001",
			Price:        99.99,
			InitialStock: 100,
		}))
		assert.Equal(t, 100, p.AvailableStock())

		require.NoError(t, p.ApplyEvent(product.StockReserved{
			ProductID: "PROD-001",
			Quantity:  30,
		}))
		assert.Equal(t, 70, p.AvailableStock())

		require.NoError(t, p.ApplyEvent(product.StockAdded{
			ProductID: "PROD-001",
			Quantity:  20,
		}))
		assert.Equal(t, 90, p.AvailableStock())
	})
}
```

### Testing Complex Business Scenarios

Create `internal/domain/order/order_test.go`:

```go
package order_test

import (
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink/testing/bdd"
	"github.com/stretchr/testify/assert"
	"minkshop/internal/domain/order"
)

func TestOrder_Place(t *testing.T) {
	t.Run("places order with valid items", func(t *testing.T) {
		o := order.NewOrder("ORDER-001")

		items := []order.OrderItem{
			{ProductID: "PROD-001", ProductName: "Widget", Quantity: 2, UnitPrice: 29.99},
			{ProductID: "PROD-002", ProductName: "Gadget", Quantity: 1, UnitPrice: 49.99},
		}

		bdd.Given(t, o).
			When(func() error {
				return o.Place("CUST-001", items, order.ShippingAddress{
					Name:       "John Doe",
					Street:     "123 Main St",
					City:       "New York",
					State:      "NY",
					PostalCode: "10001",
					Country:    "USA",
				})
			}).
			ThenMatches(func(events []interface{}) bool {
				if len(events) != 1 {
					return false
				}
				placed, ok := events[0].(order.OrderPlaced)
				return ok &&
					placed.OrderID == "ORDER-001" &&
					placed.CustomerID == "CUST-001" &&
					len(placed.Items) == 2 &&
					placed.TotalAmount > 0
			})
	})

	t.Run("rejects empty order", func(t *testing.T) {
		o := order.NewOrder("ORDER-001")

		bdd.Given(t, o).
			When(func() error {
				return o.Place("CUST-001", []order.OrderItem{}, order.ShippingAddress{})
			}).
			ThenError(order.ErrEmptyOrder)
	})

	t.Run("rejects duplicate order placement", func(t *testing.T) {
		o := order.NewOrder("ORDER-001")

		bdd.Given(t, o,
			order.OrderPlaced{
				OrderID:    "ORDER-001",
				CustomerID: "CUST-001",
				Items:      []order.OrderItem{{ProductID: "PROD-001", Quantity: 1, UnitPrice: 29.99}},
			},
		).
			When(func() error {
				return o.Place("CUST-002", []order.OrderItem{}, order.ShippingAddress{})
			}).
			ThenError(order.ErrOrderAlreadyPlaced)
	})
}

func TestOrder_Ship(t *testing.T) {
	t.Run("ships confirmed order", func(t *testing.T) {
		o := order.NewOrder("ORDER-001")

		bdd.Given(t, o,
			order.OrderPlaced{
				OrderID:    "ORDER-001",
				CustomerID: "CUST-001",
			},
			order.PaymentReceived{
				OrderID: "ORDER-001",
				Amount:  109.97,
			},
			order.OrderConfirmed{
				OrderID: "ORDER-001",
			},
		).
			When(func() error {
				return o.Ship("TRACK-12345", "FedEx")
			}).
			Then(order.OrderShipped{
				OrderID:        "ORDER-001",
				TrackingNumber: "TRACK-12345",
				Carrier:        "FedEx",
			})
	})

	t.Run("cannot ship unconfirmed order", func(t *testing.T) {
		o := order.NewOrder("ORDER-001")

		bdd.Given(t, o,
			order.OrderPlaced{OrderID: "ORDER-001", CustomerID: "CUST-001"},
		).
			When(func() error {
				return o.Ship("TRACK-12345", "FedEx")
			}).
			ThenError(order.ErrOrderNotConfirmed)
	})

	t.Run("cannot ship already shipped order", func(t *testing.T) {
		o := order.NewOrder("ORDER-001")

		bdd.Given(t, o,
			order.OrderPlaced{OrderID: "ORDER-001"},
			order.PaymentReceived{OrderID: "ORDER-001"},
			order.OrderConfirmed{OrderID: "ORDER-001"},
			order.OrderShipped{OrderID: "ORDER-001", TrackingNumber: "TRACK-001"},
		).
			When(func() error {
				return o.Ship("TRACK-002", "UPS")
			}).
			ThenError(order.ErrOrderAlreadyShipped)
	})
}

func TestOrder_Cancel(t *testing.T) {
	t.Run("cancels pending order", func(t *testing.T) {
		o := order.NewOrder("ORDER-001")

		bdd.Given(t, o,
			order.OrderPlaced{OrderID: "ORDER-001", CustomerID: "CUST-001"},
		).
			When(func() error {
				return o.Cancel("Customer changed mind")
			}).
			Then(order.OrderCancelled{
				OrderID: "ORDER-001",
				Reason:  "Customer changed mind",
			})
	})

	t.Run("cannot cancel shipped order", func(t *testing.T) {
		o := order.NewOrder("ORDER-001")

		bdd.Given(t, o,
			order.OrderPlaced{OrderID: "ORDER-001"},
			order.PaymentReceived{OrderID: "ORDER-001"},
			order.OrderConfirmed{OrderID: "ORDER-001"},
			order.OrderShipped{OrderID: "ORDER-001"},
		).
			When(func() error {
				return o.Cancel("Changed mind")
			}).
			ThenError(order.ErrCannotCancelShippedOrder)
	})
}

func TestOrder_CompleteLifecycle(t *testing.T) {
	t.Run("full order lifecycle", func(t *testing.T) {
		o := order.NewOrder("ORDER-001")

		// Given: no prior events
		bdd.Given(t, o).
			// When: place order
			When(func() error {
				return o.Place("CUST-001", []order.OrderItem{
					{ProductID: "PROD-001", ProductName: "Widget", Quantity: 2, UnitPrice: 29.99},
				}, order.ShippingAddress{
					Name: "John Doe", Street: "123 Main", City: "NYC", State: "NY", PostalCode: "10001", Country: "USA",
				})
			}).
			ThenNoError()

		// Check state after placement
		assert.Equal(t, order.StatusPending, o.Status())

		// Process payment
		bdd.Given(t, o). // Continues from current state
			When(func() error {
				return o.ReceivePayment("PAY-001", 59.98, "card")
			}).
			ThenNoError()
		
		assert.Equal(t, order.StatusPaid, o.Status())

		// Confirm order
		bdd.Given(t, o).
			When(func() error {
				return o.Confirm()
			}).
			ThenNoError()
		
		assert.Equal(t, order.StatusConfirmed, o.Status())

		// Ship order
		bdd.Given(t, o).
			When(func() error {
				return o.Ship("TRACK-123", "FedEx")
			}).
			ThenNoError()
		
		assert.Equal(t, order.StatusShipped, o.Status())

		// Deliver order
		bdd.Given(t, o).
			When(func() error {
				return o.MarkDelivered("Signed by recipient")
			}).
			ThenNoError()
		
		assert.Equal(t, order.StatusDelivered, o.Status())
	})
}
```

---

## Part 2: Testing Projections

Create `internal/projections/product_catalog_test.go`:

```go
package projections_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/testing/projections"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"minkshop/internal/domain/product"
	proj "minkshop/internal/projections"
)

func TestProductCatalogProjection(t *testing.T) {
	t.Run("creates product view from ProductCreated", func(t *testing.T) {
		// Arrange
		repo := mink.NewInMemoryRepository(func(p *proj.ProductView) string {
			return p.ProductID
		})
		projection := proj.NewProductCatalogProjection(repo)
		ctx := context.Background()

		event := projections.NewTestEvent("ProductCreated", 
			product.ProductCreated{
				ProductID:    "PROD-001",
				SKU:          "WIDGET-001",
				Name:         "Widget Pro",
				Description:  "Professional widget",
				Price:        99.99,
				InitialStock: 50,
				CreatedAt:    time.Now(),
			},
		)

		// Act
		err := projection.Apply(ctx, event)

		// Assert
		require.NoError(t, err)
		view, err := repo.Get(ctx, "PROD-001")
		require.NoError(t, err)
		assert.Equal(t, "WIDGET-001", view.SKU)
		assert.Equal(t, "Widget Pro", view.Name)
		assert.Equal(t, 99.99, view.Price)
		assert.Equal(t, 50, view.AvailableStock)
		assert.True(t, view.IsAvailable)
		assert.False(t, view.IsDiscontinued)
	})

	t.Run("updates stock on StockReserved", func(t *testing.T) {
		// Arrange
		repo := mink.NewInMemoryRepository(func(p *proj.ProductView) string {
			return p.ProductID
		})
		projection := proj.NewProductCatalogProjection(repo)
		ctx := context.Background()

		// Create initial product
		_ = repo.Insert(ctx, &proj.ProductView{
			ProductID:      "PROD-001",
			AvailableStock: 50,
			IsAvailable:    true,
		})

		event := projections.NewTestEvent("StockReserved",
			product.StockReserved{
				ProductID:  "PROD-001",
				OrderID:    "ORDER-001",
				Quantity:   10,
				ReservedAt: time.Now(),
			},
		)

		// Act
		err := projection.Apply(ctx, event)

		// Assert
		require.NoError(t, err)
		view, _ := repo.Get(ctx, "PROD-001")
		assert.Equal(t, 40, view.AvailableStock)
		assert.Equal(t, 10, view.ReservedStock)
	})

	t.Run("marks unavailable when stock depleted", func(t *testing.T) {
		// Arrange
		repo := mink.NewInMemoryRepository(func(p *proj.ProductView) string {
			return p.ProductID
		})
		projection := proj.NewProductCatalogProjection(repo)
		ctx := context.Background()

		_ = repo.Insert(ctx, &proj.ProductView{
			ProductID:      "PROD-001",
			AvailableStock: 5,
			IsAvailable:    true,
		})

		event := projections.NewTestEvent("StockReserved",
			product.StockReserved{
				ProductID: "PROD-001",
				OrderID:   "ORDER-001",
				Quantity:  5,
			},
		)

		// Act
		err := projection.Apply(ctx, event)

		// Assert
		require.NoError(t, err)
		view, _ := repo.Get(ctx, "PROD-001")
		assert.Equal(t, 0, view.AvailableStock)
		assert.False(t, view.IsAvailable)
	})
}

func TestProductCatalogProjection_EventSequence(t *testing.T) {
	t.Run("handles full product lifecycle", func(t *testing.T) {
		repo := mink.NewInMemoryRepository(func(p *proj.ProductView) string {
			return p.ProductID
		})
		projection := proj.NewProductCatalogProjection(repo)
		ctx := context.Background()
		now := time.Now()

		// Helper to create events
		makeEvent := func(eventType string, data interface{}) mink.StoredEvent {
			jsonData, _ := json.Marshal(data)
			return mink.StoredEvent{
				ID:        "evt-" + eventType,
				StreamID:  "product-PROD-001",
				Type:      eventType,
				Data:      jsonData,
				Timestamp: now,
			}
		}

		events := []mink.StoredEvent{
			makeEvent("ProductCreated", product.ProductCreated{
				ProductID: "PROD-001", SKU: "SKU-001", Name: "Widget",
				Price: 99.99, InitialStock: 100, CreatedAt: now,
			}),
			makeEvent("StockReserved", product.StockReserved{
				ProductID: "PROD-001", OrderID: "ORD-1", Quantity: 30, ReservedAt: now,
			}),
			makeEvent("StockShipped", product.StockShipped{
				ProductID: "PROD-001", OrderID: "ORD-1", Quantity: 30, ShippedAt: now,
			}),
			makeEvent("PriceChanged", product.PriceChanged{
				ProductID: "PROD-001", OldPrice: 99.99, NewPrice: 79.99, ChangedAt: now,
			}),
			makeEvent("StockAdded", product.StockAdded{
				ProductID: "PROD-001", Quantity: 50, AddedAt: now,
			}),
		}

		// Apply all events
		for _, event := range events {
			require.NoError(t, projection.Apply(ctx, event))
		}

		// Verify final state
		view, err := repo.Get(ctx, "PROD-001")
		require.NoError(t, err)

		assert.Equal(t, "Widget", view.Name)
		assert.Equal(t, 79.99, view.Price)                   // Price changed
		assert.Equal(t, 120, view.AvailableStock)            // 100 - 30 + 50
		assert.Equal(t, 0, view.ReservedStock)               // 30 reserved, 30 shipped
		assert.True(t, view.IsAvailable)
	})
}
```

---

## Part 3: Event Assertions

Use the `testing/assertions` package for detailed event matching.

Create `internal/domain/product/assertions_test.go`:

```go
package product_test

import (
	"testing"

	"github.com/AshkanYarmoradi/go-mink/testing/assertions"
	"minkshop/internal/domain/product"
)

func TestProductEvents_WithAssertions(t *testing.T) {
	t.Run("event fields match exactly", func(t *testing.T) {
		p := product.NewProduct("PROD-001")
		_ = p.Create("SKU-001", "Widget", "Description", 99.99, 50)

		events := p.UncommittedEvents()

		assertions.AssertEventTypes(t, events, "ProductCreated")
		assertions.AssertEventCount(t, events, 1)
		
		// Deep field matching
		assertions.AssertEvent(t, events[0], assertions.EventMatcher{
			Type: "ProductCreated",
			Fields: map[string]interface{}{
				"ProductID":    "PROD-001",
				"SKU":          "SKU-001",
				"Name":         "Widget",
				"Price":        99.99,
				"InitialStock": 50,
			},
		})
	})

	t.Run("multiple events in sequence", func(t *testing.T) {
		p := product.NewProduct("PROD-001")
		_ = p.Create("SKU-001", "Widget", "Desc", 99.99, 100)
		p.ClearUncommittedEvents()
		_ = p.ReserveStock("ORDER-001", 10)
		_ = p.ReserveStock("ORDER-002", 20)

		events := p.UncommittedEvents()

		assertions.AssertEventTypes(t, events, 
			"StockReserved",
			"StockReserved",
		)

		// Check each event
		assertions.AssertEvent(t, events[0], assertions.EventMatcher{
			Type: "StockReserved",
			Fields: map[string]interface{}{
				"OrderID":  "ORDER-001",
				"Quantity": 10,
			},
		})

		assertions.AssertEvent(t, events[1], assertions.EventMatcher{
			Type: "StockReserved",
			Fields: map[string]interface{}{
				"OrderID":  "ORDER-002",
				"Quantity": 20,
			},
		})
	})

	t.Run("diff shows field differences", func(t *testing.T) {
		expected := product.ProductCreated{
			ProductID:    "PROD-001",
			SKU:          "SKU-001",
			Name:         "Widget",
			Price:        99.99,
			InitialStock: 50,
		}

		actual := product.ProductCreated{
			ProductID:    "PROD-001",
			SKU:          "SKU-001",
			Name:         "Gadget",       // Different!
			Price:        79.99,           // Different!
			InitialStock: 50,
		}

		diff := assertions.DiffEvents(expected, actual)
		t.Logf("Event diff:\n%s", diff)
		// Useful for debugging test failures
	})
}
```

---

## Part 4: Integration Tests

Create `internal/integration/integration_test.go`:

```go
package integration_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/postgres"
	"github.com/AshkanYarmoradi/go-mink/testing/containers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	
	"minkshop/internal/app"
	"minkshop/internal/commands"
)

var testContainer *containers.PostgresContainer

func TestMain(m *testing.M) {
	// Skip if running short tests
	if testing.Short() {
		os.Exit(m.Run())
	}

	// Start PostgreSQL container
	ctx := context.Background()
	var err error
	testContainer, err = containers.NewPostgresContainer(ctx)
	if err != nil {
		panic("Failed to start PostgreSQL container: " + err.Error())
	}

	// Run tests
	code := m.Run()

	// Cleanup
	if err := testContainer.Terminate(ctx); err != nil {
		panic("Failed to terminate container: " + err.Error())
	}

	os.Exit(code)
}

func TestApplication_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create application with test container
	application, err := app.New(ctx, app.Config{
		DatabaseURL: testContainer.ConnectionString(),
	})
	require.NoError(t, err)
	defer application.Close()

	t.Run("creates and retrieves product", func(t *testing.T) {
		// Create product
		cmd := &commands.CreateProduct{
			ProductID:    "PROD-INT-001",
			SKU:          "INT-SKU-001",
			Name:         "Integration Test Widget",
			Description:  "A widget for integration testing",
			Price:        49.99,
			InitialStock: 25,
		}

		result, err := application.CommandBus.Dispatch(ctx, cmd)
		require.NoError(t, err)
		assert.False(t, result.IsError())
		assert.Equal(t, "PROD-INT-001", result.AggregateID)

		// Give projections time to process
		time.Sleep(100 * time.Millisecond)

		// Query the product
		view, err := application.ProductRepo.Get(ctx, "PROD-INT-001")
		require.NoError(t, err)
		assert.Equal(t, "Integration Test Widget", view.Name)
		assert.Equal(t, 49.99, view.Price)
		assert.Equal(t, 25, view.AvailableStock)
	})

	t.Run("handles concurrent stock reservations", func(t *testing.T) {
		// Create product
		createCmd := &commands.CreateProduct{
			ProductID:    "PROD-CONCURRENT",
			SKU:          "CONC-001",
			Name:         "Concurrent Test Product",
			Price:        19.99,
			InitialStock: 10,
		}
		_, err := application.CommandBus.Dispatch(ctx, createCmd)
		require.NoError(t, err)

		// Try to reserve more than available concurrently
		reserveCmd1 := &commands.ReserveStock{
			ProductID: "PROD-CONCURRENT",
			OrderID:   "ORDER-C1",
			Quantity:  7,
		}
		reserveCmd2 := &commands.ReserveStock{
			ProductID: "PROD-CONCURRENT",
			OrderID:   "ORDER-C2",
			Quantity:  7,
		}

		// One should succeed, one should fail
		result1, err1 := application.CommandBus.Dispatch(ctx, reserveCmd1)
		result2, err2 := application.CommandBus.Dispatch(ctx, reserveCmd2)

		// Check that exactly one succeeded
		success := 0
		if err1 == nil && !result1.IsError() {
			success++
		}
		if err2 == nil && !result2.IsError() {
			success++
		}

		assert.Equal(t, 1, success, "Exactly one reservation should succeed")
	})

	t.Run("full order flow", func(t *testing.T) {
		// 1. Create product
		createProductCmd := &commands.CreateProduct{
			ProductID:    "PROD-FLOW-001",
			SKU:          "FLOW-001",
			Name:         "Flow Test Product",
			Price:        29.99,
			InitialStock: 100,
		}
		_, err := application.CommandBus.Dispatch(ctx, createProductCmd)
		require.NoError(t, err)

		// 2. Create cart and add items
		addToCartCmd := &commands.AddToCart{
			CartID:      "CART-FLOW-001",
			CustomerID:  "CUST-FLOW-001",
			ProductID:   "PROD-FLOW-001",
			ProductName: "Flow Test Product",
			Quantity:    2,
			UnitPrice:   29.99,
		}
		_, err = application.CommandBus.Dispatch(ctx, addToCartCmd)
		require.NoError(t, err)

		// 3. Checkout cart
		checkoutCmd := &commands.CheckoutCart{
			CartID: "CART-FLOW-001",
			ShippingAddress: commands.AddressDTO{
				Name:       "Test User",
				Street:     "123 Test St",
				City:       "Test City",
				State:      "TS",
				PostalCode: "12345",
				Country:    "Testland",
			},
		}
		result, err := application.CommandBus.Dispatch(ctx, checkoutCmd)
		require.NoError(t, err)
		orderID := result.AggregateID

		// 4. Process payment
		paymentCmd := &commands.ReceivePayment{
			OrderID:       orderID,
			TransactionID: "TXN-001",
			Amount:        59.98,
			PaymentMethod: "card",
		}
		_, err = application.CommandBus.Dispatch(ctx, paymentCmd)
		require.NoError(t, err)

		// 5. Confirm order
		confirmCmd := &commands.ConfirmOrder{OrderID: orderID}
		_, err = application.CommandBus.Dispatch(ctx, confirmCmd)
		require.NoError(t, err)

		// 6. Ship order
		shipCmd := &commands.ShipOrder{
			OrderID:        orderID,
			TrackingNumber: "TRACK-FLOW-001",
			Carrier:        "TestCarrier",
		}
		_, err = application.CommandBus.Dispatch(ctx, shipCmd)
		require.NoError(t, err)

		// Give projections time
		time.Sleep(200 * time.Millisecond)

		// 7. Verify order state
		orderView, err := application.OrderRepo.Get(ctx, orderID)
		require.NoError(t, err)
		assert.Equal(t, "shipped", orderView.Status)
		assert.Equal(t, "TRACK-FLOW-001", orderView.TrackingNumber)
		assert.Equal(t, 2, orderView.ItemCount)

		// 8. Verify product stock was reduced
		productView, err := application.ProductRepo.Get(ctx, "PROD-FLOW-001")
		require.NoError(t, err)
		assert.Equal(t, 98, productView.AvailableStock) // 100 - 2
	})
}

func TestEventStore_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()

	// Create adapter
	adapter, err := postgres.NewAdapter(testContainer.ConnectionString())
	require.NoError(t, err)
	defer adapter.Close()

	require.NoError(t, adapter.Initialize(ctx))

	store := mink.New(adapter)

	t.Run("appends and loads events", func(t *testing.T) {
		streamID := "test-stream-" + time.Now().Format("150405")

		// Append events
		events := []mink.EventData{
			{Type: "TestCreated", Data: []byte(`{"id":"1"}`)},
			{Type: "TestUpdated", Data: []byte(`{"id":"1","value":"updated"}`)},
		}

		stored, err := adapter.Append(ctx, streamID, events, mink.NoStream)
		require.NoError(t, err)
		assert.Len(t, stored, 2)
		assert.Equal(t, int64(1), stored[0].Version)
		assert.Equal(t, int64(2), stored[1].Version)

		// Load events
		loaded, err := adapter.Load(ctx, streamID, 0)
		require.NoError(t, err)
		assert.Len(t, loaded, 2)
		assert.Equal(t, "TestCreated", loaded[0].Type)
		assert.Equal(t, "TestUpdated", loaded[1].Type)
	})

	t.Run("handles optimistic concurrency", func(t *testing.T) {
		streamID := "concurrency-test-" + time.Now().Format("150405")

		// First append
		_, err := adapter.Append(ctx, streamID, []mink.EventData{
			{Type: "Event1", Data: []byte(`{}`)},
		}, mink.NoStream)
		require.NoError(t, err)

		// Second append with wrong version
		_, err = adapter.Append(ctx, streamID, []mink.EventData{
			{Type: "Event2", Data: []byte(`{}`)},
		}, mink.NoStream) // Should be version 1

		require.Error(t, err)
		assert.ErrorIs(t, err, mink.ErrConcurrencyConflict)
	})
}
```

---

## Part 5: Test Helpers

Create shared test utilities in `internal/testing/helpers.go`:

```go
package testing

import (
	"context"
	"testing"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"minkshop/internal/domain/product"
	"minkshop/internal/domain/cart"
	"minkshop/internal/domain/order"
)

// TestFixture provides common test setup.
type TestFixture struct {
	Store      *mink.EventStore
	Adapter    *memory.MemoryAdapter
	Ctx        context.Context
	CancelFunc context.CancelFunc
}

// NewTestFixture creates a new test fixture.
func NewTestFixture(t *testing.T) *TestFixture {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	adapter := memory.NewAdapter()
	store := mink.New(adapter)

	// Register all events
	store.RegisterEvents(product.AllEvents()...)
	store.RegisterEvents(cart.AllEvents()...)
	store.RegisterEvents(order.AllEvents()...)

	t.Cleanup(func() {
		cancel()
	})

	return &TestFixture{
		Store:      store,
		Adapter:    adapter,
		Ctx:        ctx,
		CancelFunc: cancel,
	}
}

// CreateProduct is a helper to create a product in tests.
func (f *TestFixture) CreateProduct(t *testing.T, id, sku, name string, price float64, stock int) *product.Product {
	t.Helper()

	p := product.NewProduct(id)
	if err := p.Create(sku, name, "Test product", price, stock); err != nil {
		t.Fatalf("Failed to create product: %v", err)
	}
	if err := f.Store.SaveAggregate(f.Ctx, p); err != nil {
		t.Fatalf("Failed to save product: %v", err)
	}
	return p
}

// CreateOrder is a helper to create an order in tests.
func (f *TestFixture) CreateOrder(t *testing.T, orderID, customerID string, items []order.OrderItem) *order.Order {
	t.Helper()

	o := order.NewOrder(orderID)
	if err := o.Place(customerID, items, order.ShippingAddress{
		Name: "Test", Street: "123 Test", City: "Test", State: "TS", PostalCode: "12345", Country: "Test",
	}); err != nil {
		t.Fatalf("Failed to place order: %v", err)
	}
	if err := f.Store.SaveAggregate(f.Ctx, o); err != nil {
		t.Fatalf("Failed to save order: %v", err)
	}
	return o
}
```

---

## Running Tests

```bash
# Run unit tests only
go test -short ./...

# Run all tests (requires Docker for integration tests)
go test ./...

# Run tests with coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Run specific test
go test -run TestProduct_Create ./internal/domain/product/

# Run tests with verbose output
go test -v ./internal/domain/...

# Run benchmark tests
go test -bench=. ./internal/...
```

---

## What's Next?

You've built comprehensive tests with:

- ✅ BDD-style aggregate testing
- ✅ Projection testing with fixtures
- ✅ Event assertions and diffing
- ✅ Integration tests with containers
- ✅ Test helpers and fixtures

In **Part 6: Production Ready**, you'll:

- Add Prometheus metrics
- Integrate OpenTelemetry tracing
- Handle errors gracefully
- Deploy with Docker

<div class="code-example" markdown="1">

**Next**: [Part 6: Production Ready →](/tutorial/06-production)

</div>

---

## Testing Patterns Summary

| Pattern | When to Use |
|---------|-------------|
| **BDD Given/When/Then** | Aggregate behavior tests |
| **Event Assertions** | Verifying exact event output |
| **Projection Fixtures** | Testing read model updates |
| **Integration Tests** | End-to-end flows |
| **Test Containers** | Database integration |

---

{: .highlight }
> 💡 **Best Practice**: Run unit tests on every commit, integration tests in CI. Use test containers to ensure production-like behavior without external dependencies.
