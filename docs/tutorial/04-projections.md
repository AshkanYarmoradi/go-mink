---
layout: default
title: "Part 4: Projections & Queries"
parent: "Tutorial: Building an E-Commerce App"
nav_order: 4
permalink: /tutorial/04-projections
---

# Part 4: Projections & Queries
{: .no_toc }

Build optimized read models from your event stream.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## Overview

In this part, you'll:

- Understand projections and read models
- Build inline projections for consistent reads
- Create async projections for scalability
- Add live projections for real-time updates
- Query read models with filters and pagination

**Time**: ~45 minutes

---

## What are Projections?

Projections transform your event stream into optimized read models:

```
Events                          Projections                    Read Models
─────────────────────────────────────────────────────────────────────────────
OrderPlaced ──────┐
ItemAdded    ─────┤             ┌──────────────────┐
OrderShipped ─────┼────────────▶│ Order Summary    │────────▶ OrderSummaryView
OrderDelivered ───┘             │ Projection       │          (Fast queries)
                                └──────────────────┘
                                
ProductCreated ───┐             ┌──────────────────┐
StockAdded    ────┼────────────▶│ Product Catalog  │────────▶ ProductCatalog
PriceChanged ─────┘             │ Projection       │          (Search ready)
                                └──────────────────┘

All Events ───────────────────▶ ┌──────────────────┐
                                │ Analytics        │────────▶ Dashboard
                                │ Projection       │          (Real-time)
                                └──────────────────┘
```

### Types of Projections

| Type | Consistency | Performance | Use Case |
|------|------------|-------------|----------|
| **Inline** | Strong | Lower write throughput | Critical reads (account balance) |
| **Async** | Eventual | High throughput | Reports, search, analytics |
| **Live** | Real-time | Transient | Dashboards, notifications |

---

## Part 1: Define Read Models

Let's create read models for our e-commerce system.

### Product Catalog Read Model

Create `internal/projections/product_catalog.go`:

```go
package projections

import (
	"context"
	"encoding/json"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"minkshop/internal/domain/product"
)

// ProductView represents a product in the catalog.
type ProductView struct {
	ProductID      string    `json:"productId"`
	SKU            string    `json:"sku"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Price          float64   `json:"price"`
	AvailableStock int       `json:"availableStock"`
	ReservedStock  int       `json:"reservedStock"`
	IsAvailable    bool      `json:"isAvailable"`
	IsDiscontinued bool      `json:"isDiscontinued"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// ProductCatalogProjection builds the product catalog read model.
type ProductCatalogProjection struct {
	mink.ProjectionBase
	repo *mink.InMemoryRepository[ProductView]
}

// NewProductCatalogProjection creates a new projection.
func NewProductCatalogProjection(repo *mink.InMemoryRepository[ProductView]) *ProductCatalogProjection {
	return &ProductCatalogProjection{
		ProjectionBase: mink.NewProjectionBase("ProductCatalog",
			"ProductCreated",
			"PriceChanged",
			"StockAdded",
			"StockReserved",
			"StockReleased",
			"StockShipped",
			"ProductDiscontinued",
		),
		repo: repo,
	}
}

// Apply processes an event and updates the read model.
func (p *ProductCatalogProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
	switch event.Type {
	case "ProductCreated":
		var e product.ProductCreated
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Insert(ctx, &ProductView{
			ProductID:      e.ProductID,
			SKU:            e.SKU,
			Name:           e.Name,
			Description:    e.Description,
			Price:          e.Price,
			AvailableStock: e.InitialStock,
			IsAvailable:    e.InitialStock > 0,
			CreatedAt:      e.CreatedAt,
			UpdatedAt:      e.CreatedAt,
		})

	case "PriceChanged":
		var e product.PriceChanged
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.ProductID, func(v *ProductView) {
			v.Price = e.NewPrice
			v.UpdatedAt = e.ChangedAt
		})

	case "StockAdded":
		var e product.StockAdded
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.ProductID, func(v *ProductView) {
			v.AvailableStock += e.Quantity
			v.IsAvailable = !v.IsDiscontinued && v.AvailableStock > 0
			v.UpdatedAt = e.AddedAt
		})

	case "StockReserved":
		var e product.StockReserved
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.ProductID, func(v *ProductView) {
			v.AvailableStock -= e.Quantity
			v.ReservedStock += e.Quantity
			v.IsAvailable = !v.IsDiscontinued && v.AvailableStock > 0
			v.UpdatedAt = e.ReservedAt
		})

	case "StockReleased":
		var e product.StockReleased
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.ProductID, func(v *ProductView) {
			v.AvailableStock += e.Quantity
			v.ReservedStock -= e.Quantity
			v.IsAvailable = !v.IsDiscontinued && v.AvailableStock > 0
			v.UpdatedAt = e.ReleasedAt
		})

	case "StockShipped":
		var e product.StockShipped
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.ProductID, func(v *ProductView) {
			v.ReservedStock -= e.Quantity
			v.UpdatedAt = e.ShippedAt
		})

	case "ProductDiscontinued":
		var e product.ProductDiscontinued
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.ProductID, func(v *ProductView) {
			v.IsDiscontinued = true
			v.IsAvailable = false
			v.UpdatedAt = e.DiscontinuedAt
		})
	}

	return nil
}
```

### Order History Read Model

Create `internal/projections/order_history.go`:

```go
package projections

import (
	"context"
	"encoding/json"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"minkshop/internal/domain/order"
)

// OrderView represents an order in the history.
type OrderView struct {
	OrderID        string             `json:"orderId"`
	CustomerID     string             `json:"customerId"`
	Status         string             `json:"status"`
	Items          []OrderItemView    `json:"items"`
	ItemCount      int                `json:"itemCount"`
	Subtotal       float64            `json:"subtotal"`
	ShippingCost   float64            `json:"shippingCost"`
	Tax            float64            `json:"tax"`
	TotalAmount    float64            `json:"totalAmount"`
	ShippingAddress AddressView       `json:"shippingAddress"`
	TrackingNumber string             `json:"trackingNumber,omitempty"`
	Carrier        string             `json:"carrier,omitempty"`
	PlacedAt       time.Time          `json:"placedAt"`
	PaidAt         *time.Time         `json:"paidAt,omitempty"`
	ShippedAt      *time.Time         `json:"shippedAt,omitempty"`
	DeliveredAt    *time.Time         `json:"deliveredAt,omitempty"`
	CancelledAt    *time.Time         `json:"cancelledAt,omitempty"`
	UpdatedAt      time.Time          `json:"updatedAt"`
}

// OrderItemView represents an item in an order view.
type OrderItemView struct {
	ProductID   string  `json:"productId"`
	ProductName string  `json:"productName"`
	Quantity    int     `json:"quantity"`
	UnitPrice   float64 `json:"unitPrice"`
	Subtotal    float64 `json:"subtotal"`
}

// AddressView represents a shipping address.
type AddressView struct {
	Name       string `json:"name"`
	Street     string `json:"street"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postalCode"`
	Country    string `json:"country"`
}

// OrderHistoryProjection builds the order history read model.
type OrderHistoryProjection struct {
	mink.ProjectionBase
	repo *mink.InMemoryRepository[OrderView]
}

// NewOrderHistoryProjection creates a new projection.
func NewOrderHistoryProjection(repo *mink.InMemoryRepository[OrderView]) *OrderHistoryProjection {
	return &OrderHistoryProjection{
		ProjectionBase: mink.NewProjectionBase("OrderHistory",
			"OrderPlaced",
			"PaymentReceived",
			"OrderConfirmed",
			"OrderShipped",
			"OrderDelivered",
			"OrderCancelled",
		),
		repo: repo,
	}
}

// Apply processes an event and updates the read model.
func (p *OrderHistoryProjection) Apply(ctx context.Context, event mink.StoredEvent) error {
	switch event.Type {
	case "OrderPlaced":
		var e order.OrderPlaced
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		
		items := make([]OrderItemView, len(e.Items))
		itemCount := 0
		for i, item := range e.Items {
			items[i] = OrderItemView{
				ProductID:   item.ProductID,
				ProductName: item.ProductName,
				Quantity:    item.Quantity,
				UnitPrice:   item.UnitPrice,
				Subtotal:    item.Subtotal,
			}
			itemCount += item.Quantity
		}
		
		return p.repo.Insert(ctx, &OrderView{
			OrderID:      e.OrderID,
			CustomerID:   e.CustomerID,
			Status:       "pending",
			Items:        items,
			ItemCount:    itemCount,
			Subtotal:     e.Subtotal,
			ShippingCost: e.ShippingCost,
			Tax:          e.Tax,
			TotalAmount:  e.TotalAmount,
			ShippingAddress: AddressView{
				Name:       e.ShippingAddress.Name,
				Street:     e.ShippingAddress.Street,
				City:       e.ShippingAddress.City,
				State:      e.ShippingAddress.State,
				PostalCode: e.ShippingAddress.PostalCode,
				Country:    e.ShippingAddress.Country,
			},
			PlacedAt:  e.PlacedAt,
			UpdatedAt: e.PlacedAt,
		})

	case "PaymentReceived":
		var e order.PaymentReceived
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.OrderID, func(v *OrderView) {
			v.Status = "paid"
			v.PaidAt = &e.ReceivedAt
			v.UpdatedAt = e.ReceivedAt
		})

	case "OrderConfirmed":
		var e order.OrderConfirmed
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.OrderID, func(v *OrderView) {
			v.Status = "confirmed"
			v.UpdatedAt = e.ConfirmedAt
		})

	case "OrderShipped":
		var e order.OrderShipped
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.OrderID, func(v *OrderView) {
			v.Status = "shipped"
			v.TrackingNumber = e.TrackingNumber
			v.Carrier = e.Carrier
			v.ShippedAt = &e.ShippedAt
			v.UpdatedAt = e.ShippedAt
		})

	case "OrderDelivered":
		var e order.OrderDelivered
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.OrderID, func(v *OrderView) {
			v.Status = "delivered"
			v.DeliveredAt = &e.DeliveredAt
			v.UpdatedAt = e.DeliveredAt
		})

	case "OrderCancelled":
		var e order.OrderCancelled
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return err
		}
		return p.repo.Update(ctx, e.OrderID, func(v *OrderView) {
			v.Status = "cancelled"
			v.CancelledAt = &e.CancelledAt
			v.UpdatedAt = e.CancelledAt
		})
	}

	return nil
}
```

### Sales Dashboard (Live Projection)

Create `internal/projections/dashboard.go`:

```go
package projections

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"minkshop/internal/domain/order"
	"minkshop/internal/domain/product"
)

// DashboardStats holds real-time statistics.
type DashboardStats struct {
	mu sync.RWMutex
	
	// Orders
	TotalOrders      int     `json:"totalOrders"`
	PendingOrders    int     `json:"pendingOrders"`
	ShippedOrders    int     `json:"shippedOrders"`
	TotalRevenue     float64 `json:"totalRevenue"`
	
	// Products
	TotalProducts    int     `json:"totalProducts"`
	LowStockProducts int     `json:"lowStockProducts"`
	
	// Recent activity
	RecentActivity   []ActivityItem `json:"recentActivity"`
}

// ActivityItem represents a dashboard activity.
type ActivityItem struct {
	Timestamp   time.Time `json:"timestamp"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
}

// DashboardProjection provides real-time updates.
type DashboardProjection struct {
	mink.LiveProjectionBase
	stats    *DashboardStats
	updates  chan DashboardUpdate
}

// DashboardUpdate represents a dashboard update notification.
type DashboardUpdate struct {
	Type    string           `json:"type"`
	Message string           `json:"message"`
	Stats   *DashboardStats  `json:"stats"`
	Time    time.Time        `json:"time"`
}

// NewDashboardProjection creates a new live dashboard projection.
func NewDashboardProjection() *DashboardProjection {
	return &DashboardProjection{
		LiveProjectionBase: mink.NewLiveProjectionBase("Dashboard", true,
			"OrderPlaced",
			"OrderShipped",
			"OrderCancelled",
			"ProductCreated",
			"StockAdded",
			"StockReserved",
		),
		stats: &DashboardStats{
			RecentActivity: make([]ActivityItem, 0, 10),
		},
		updates: make(chan DashboardUpdate, 100),
	}
}

// OnEvent handles real-time events.
func (p *DashboardProjection) OnEvent(ctx context.Context, event mink.StoredEvent) {
	p.stats.mu.Lock()
	defer p.stats.mu.Unlock()

	var message string
	var activityType string

	switch event.Type {
	case "OrderPlaced":
		var e order.OrderPlaced
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return
		}
		p.stats.TotalOrders++
		p.stats.PendingOrders++
		p.stats.TotalRevenue += e.TotalAmount
		message = fmt.Sprintf("New order #%s for $%.2f", e.OrderID, e.TotalAmount)
		activityType = "order_placed"

	case "OrderShipped":
		var e order.OrderShipped
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return
		}
		p.stats.PendingOrders--
		p.stats.ShippedOrders++
		message = fmt.Sprintf("Order #%s shipped via %s", e.OrderID, e.Carrier)
		activityType = "order_shipped"

	case "OrderCancelled":
		var e order.OrderCancelled
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return
		}
		p.stats.PendingOrders--
		message = fmt.Sprintf("Order #%s cancelled: %s", e.OrderID, e.Reason)
		activityType = "order_cancelled"

	case "ProductCreated":
		var e product.ProductCreated
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return
		}
		p.stats.TotalProducts++
		if e.InitialStock < 10 {
			p.stats.LowStockProducts++
		}
		message = fmt.Sprintf("New product: %s ($%.2f)", e.Name, e.Price)
		activityType = "product_created"

	case "StockAdded":
		var e product.StockAdded
		if err := json.Unmarshal(event.Data, &e); err != nil {
			return
		}
		message = fmt.Sprintf("Stock added: %d units to %s", e.Quantity, e.ProductID)
		activityType = "stock_added"
	}

	if message != "" {
		// Add to recent activity (keep last 10)
		activity := ActivityItem{
			Timestamp:   event.Timestamp,
			Type:        activityType,
			Description: message,
		}
		p.stats.RecentActivity = append([]ActivityItem{activity}, p.stats.RecentActivity...)
		if len(p.stats.RecentActivity) > 10 {
			p.stats.RecentActivity = p.stats.RecentActivity[:10]
		}

		// Send update notification
		select {
		case p.updates <- DashboardUpdate{
			Type:    activityType,
			Message: message,
			Stats:   p.copyStats(),
			Time:    event.Timestamp,
		}:
		default:
			// Channel full, skip
		}
	}
}

// Updates returns the channel for dashboard updates.
func (p *DashboardProjection) Updates() <-chan DashboardUpdate {
	return p.updates
}

// GetStats returns a copy of current statistics.
func (p *DashboardProjection) GetStats() *DashboardStats {
	p.stats.mu.RLock()
	defer p.stats.mu.RUnlock()
	return p.copyStats()
}

func (p *DashboardProjection) copyStats() *DashboardStats {
	activity := make([]ActivityItem, len(p.stats.RecentActivity))
	copy(activity, p.stats.RecentActivity)
	
	return &DashboardStats{
		TotalOrders:      p.stats.TotalOrders,
		PendingOrders:    p.stats.PendingOrders,
		ShippedOrders:    p.stats.ShippedOrders,
		TotalRevenue:     p.stats.TotalRevenue,
		TotalProducts:    p.stats.TotalProducts,
		LowStockProducts: p.stats.LowStockProducts,
		RecentActivity:   activity,
	}
}
```

---

## Part 2: Set Up Projection Engine

Update the application to include projections.

Update `internal/app/app.go`:

```go
package app

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/AshkanYarmoradi/go-mink/adapters/postgres"
	
	"minkshop/internal/domain/cart"
	"minkshop/internal/domain/order"
	"minkshop/internal/domain/product"
	"minkshop/internal/handlers"
	"minkshop/internal/projections"
)

// Application holds all application dependencies.
type Application struct {
	Store            *mink.EventStore
	CommandBus       *mink.CommandBus
	ProjectionEngine *mink.ProjectionEngine
	
	// Read model repositories
	ProductRepo *mink.InMemoryRepository[projections.ProductView]
	OrderRepo   *mink.InMemoryRepository[projections.OrderView]
	Dashboard   *projections.DashboardProjection
	
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

	// Create read model repositories
	productRepo := mink.NewInMemoryRepository(func(p *projections.ProductView) string {
		return p.ProductID
	})
	
	orderRepo := mink.NewInMemoryRepository(func(o *projections.OrderView) string {
		return o.OrderID
	})

	// Create checkpoint store
	checkpointStore := memory.NewCheckpointStore()

	// Create projection engine
	engine := mink.NewProjectionEngine(store,
		mink.WithCheckpointStore(checkpointStore),
	)

	// Register inline projections (synchronous)
	productCatalog := projections.NewProductCatalogProjection(productRepo)
	if err := engine.RegisterInline(productCatalog); err != nil {
		adapter.Close()
		return nil, fmt.Errorf("failed to register product catalog: %w", err)
	}

	orderHistory := projections.NewOrderHistoryProjection(orderRepo)
	if err := engine.RegisterInline(orderHistory); err != nil {
		adapter.Close()
		return nil, fmt.Errorf("failed to register order history: %w", err)
	}

	// Register live projection (real-time)
	dashboard := projections.NewDashboardProjection()
	if err := engine.RegisterLive(dashboard); err != nil {
		adapter.Close()
		return nil, fmt.Errorf("failed to register dashboard: %w", err)
	}

	// Start projection engine
	if err := engine.Start(ctx); err != nil {
		adapter.Close()
		return nil, fmt.Errorf("failed to start projection engine: %w", err)
	}

	// Create command bus with middleware
	bus := mink.NewCommandBus()
	bus.Use(
		mink.RecoveryMiddleware(),
		mink.ValidationMiddleware(),
		NewLoggingMiddleware(),
		NewTimingMiddleware(),
		NewProjectionMiddleware(engine), // Process projections after commands
	)

	// Register command handlers
	productHandler := handlers.NewProductHandler(store)
	productHandler.RegisterHandlers(bus)

	cartHandler := handlers.NewCartHandler(store)
	cartHandler.RegisterHandlers(bus)

	orderHandler := handlers.NewOrderHandler(store)
	orderHandler.RegisterHandlers(bus)

	return &Application{
		Store:            store,
		CommandBus:       bus,
		ProjectionEngine: engine,
		ProductRepo:      productRepo,
		OrderRepo:        orderRepo,
		Dashboard:        dashboard,
		adapter:          adapter,
	}, nil
}

// Close releases all resources.
func (a *Application) Close() error {
	if a.ProjectionEngine != nil {
		a.ProjectionEngine.Stop(context.Background())
	}
	if a.adapter != nil {
		return a.adapter.Close()
	}
	return nil
}

// NewProjectionMiddleware processes events through projections.
func NewProjectionMiddleware(engine *mink.ProjectionEngine) mink.Middleware {
	return func(next mink.MiddlewareFunc) mink.MiddlewareFunc {
		return func(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
			result, err := next(ctx, cmd)
			
			// After successful command, trigger projection processing
			// In production, this would be handled by subscriptions
			// For this tutorial, we'll process manually
			
			return result, err
		}
	}
}

// Rest of middleware functions...
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

## Part 3: Create Query Services

Create query services for read models.

Create `internal/queries/product_queries.go`:

```go
package queries

import (
	"context"

	"github.com/AshkanYarmoradi/go-mink"
	"minkshop/internal/projections"
)

// ProductQueryService provides product catalog queries.
type ProductQueryService struct {
	repo *mink.InMemoryRepository[projections.ProductView]
}

// NewProductQueryService creates a new query service.
func NewProductQueryService(repo *mink.InMemoryRepository[projections.ProductView]) *ProductQueryService {
	return &ProductQueryService{repo: repo}
}

// GetProduct returns a product by ID.
func (s *ProductQueryService) GetProduct(ctx context.Context, productID string) (*projections.ProductView, error) {
	return s.repo.Get(ctx, productID)
}

// ListProducts returns all products.
func (s *ProductQueryService) ListProducts(ctx context.Context) ([]*projections.ProductView, error) {
	return s.repo.Query(ctx, mink.NewQuery())
}

// ListAvailableProducts returns only available products.
func (s *ProductQueryService) ListAvailableProducts(ctx context.Context) ([]*projections.ProductView, error) {
	return s.repo.Query(ctx, mink.NewQuery().
		Where("IsAvailable", mink.Eq, true).
		OrderByDesc("CreatedAt"))
}

// SearchProducts searches products by name.
func (s *ProductQueryService) SearchProducts(ctx context.Context, term string) ([]*projections.ProductView, error) {
	return s.repo.Query(ctx, mink.NewQuery().
		Where("Name", mink.Contains, term).
		Where("IsAvailable", mink.Eq, true))
}

// GetLowStockProducts returns products with low stock.
func (s *ProductQueryService) GetLowStockProducts(ctx context.Context, threshold int) ([]*projections.ProductView, error) {
	return s.repo.Query(ctx, mink.NewQuery().
		Where("AvailableStock", mink.Lt, threshold).
		Where("IsDiscontinued", mink.Eq, false))
}

// GetProductsBySKU returns products matching SKU prefix.
func (s *ProductQueryService) GetProductsBySKU(ctx context.Context, skuPrefix string) ([]*projections.ProductView, error) {
	return s.repo.Query(ctx, mink.NewQuery().
		Where("SKU", mink.Contains, skuPrefix))
}
```

Create `internal/queries/order_queries.go`:

```go
package queries

import (
	"context"

	"github.com/AshkanYarmoradi/go-mink"
	"minkshop/internal/projections"
)

// OrderQueryService provides order history queries.
type OrderQueryService struct {
	repo *mink.InMemoryRepository[projections.OrderView]
}

// NewOrderQueryService creates a new query service.
func NewOrderQueryService(repo *mink.InMemoryRepository[projections.OrderView]) *OrderQueryService {
	return &OrderQueryService{repo: repo}
}

// GetOrder returns an order by ID.
func (s *OrderQueryService) GetOrder(ctx context.Context, orderID string) (*projections.OrderView, error) {
	return s.repo.Get(ctx, orderID)
}

// GetCustomerOrders returns all orders for a customer.
func (s *OrderQueryService) GetCustomerOrders(ctx context.Context, customerID string) ([]*projections.OrderView, error) {
	return s.repo.Query(ctx, mink.NewQuery().
		Where("CustomerID", mink.Eq, customerID).
		OrderByDesc("PlacedAt"))
}

// GetPendingOrders returns orders awaiting shipment.
func (s *OrderQueryService) GetPendingOrders(ctx context.Context) ([]*projections.OrderView, error) {
	return s.repo.Query(ctx, mink.NewQuery().
		Where("Status", mink.In, []string{"pending", "paid", "confirmed"}).
		OrderBy("PlacedAt"))
}

// GetShippedOrders returns orders that have been shipped.
func (s *OrderQueryService) GetShippedOrders(ctx context.Context) ([]*projections.OrderView, error) {
	return s.repo.Query(ctx, mink.NewQuery().
		Where("Status", mink.Eq, "shipped"))
}

// GetOrdersByStatus returns orders filtered by status.
func (s *OrderQueryService) GetOrdersByStatus(ctx context.Context, status string) ([]*projections.OrderView, error) {
	return s.repo.Query(ctx, mink.NewQuery().
		Where("Status", mink.Eq, status).
		OrderByDesc("UpdatedAt"))
}

// GetRecentOrders returns the most recent orders.
func (s *OrderQueryService) GetRecentOrders(ctx context.Context, limit int) ([]*projections.OrderView, error) {
	return s.repo.Query(ctx, mink.NewQuery().
		OrderByDesc("PlacedAt").
		WithLimit(limit))
}

// GetOrdersByDateRange returns orders within a date range.
// Note: For production, use a database with proper date filtering.
func (s *OrderQueryService) GetHighValueOrders(ctx context.Context, minAmount float64) ([]*projections.OrderView, error) {
	return s.repo.Query(ctx, mink.NewQuery().
		Where("TotalAmount", mink.Gte, minAmount).
		OrderByDesc("TotalAmount"))
}
```

---

## Part 4: Update Demo with Queries

Update the demo to show queries in action.

Update `cmd/server/main.go` (add after demo commands):

```go
func runDemo(ctx context.Context, app *app.Application) error {
	// ... existing demo code ...
	
	fmt.Println()
	fmt.Println("🔍 Running queries...")
	fmt.Println()

	// Query products
	productQueries := queries.NewProductQueryService(app.ProductRepo)
	
	fmt.Println("1. Get product by ID...")
	prod, err := productQueries.GetProduct(ctx, "PROD-001")
	if err != nil {
		log.Printf("   ⚠️  Product not found: %v", err)
	} else {
		fmt.Printf("   Product: %s - $%.2f (stock: %d)\n", 
			prod.Name, prod.Price, prod.AvailableStock)
	}

	fmt.Println("2. List available products...")
	products, err := productQueries.ListAvailableProducts(ctx)
	if err != nil {
		log.Printf("   ⚠️  Query failed: %v", err)
	} else {
		fmt.Printf("   Found %d available products\n", len(products))
		for _, p := range products {
			fmt.Printf("   - %s: $%.2f\n", p.Name, p.Price)
		}
	}

	// Query orders
	orderQueries := queries.NewOrderQueryService(app.OrderRepo)
	
	fmt.Println("3. Get customer orders...")
	orders, err := orderQueries.GetCustomerOrders(ctx, "CUST-001")
	if err != nil {
		log.Printf("   ⚠️  Query failed: %v", err)
	} else {
		fmt.Printf("   Customer has %d orders\n", len(orders))
		for _, o := range orders {
			fmt.Printf("   - Order %s: $%.2f (%s)\n", 
				o.OrderID, o.TotalAmount, o.Status)
		}
	}

	// Dashboard stats
	fmt.Println("4. Dashboard statistics...")
	stats := app.Dashboard.GetStats()
	fmt.Printf("   Total Orders: %d\n", stats.TotalOrders)
	fmt.Printf("   Pending: %d, Shipped: %d\n", stats.PendingOrders, stats.ShippedOrders)
	fmt.Printf("   Total Revenue: $%.2f\n", stats.TotalRevenue)
	fmt.Printf("   Products: %d (low stock: %d)\n", stats.TotalProducts, stats.LowStockProducts)
	
	if len(stats.RecentActivity) > 0 {
		fmt.Println("   Recent Activity:")
		for _, activity := range stats.RecentActivity[:min(3, len(stats.RecentActivity))] {
			fmt.Printf("     - %s\n", activity.Description)
		}
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

---

## Part 5: Process Projections

To ensure projections are updated, we need to process events. Add a helper function.

Add to `internal/app/app.go`:

```go
// ProcessProjectionsForStream manually processes projections for a stream.
// In production, this would be handled automatically by subscriptions.
func (a *Application) ProcessProjectionsForStream(ctx context.Context, streamID string) error {
	events, err := a.Store.LoadRaw(ctx, streamID, 0)
	if err != nil {
		return err
	}
	
	a.ProjectionEngine.ProcessInlineProjections(ctx, events)
	a.ProjectionEngine.NotifyLiveProjections(ctx, events)
	
	return nil
}
```

Update handlers to call this after saving. For example, in `product_handler.go`:

```go
func (h *ProductHandler) HandleCreateProduct(ctx context.Context, cmd mink.Command) (mink.CommandResult, error) {
	// ... existing code ...

	// Save to event store
	if err := h.store.SaveAggregate(ctx, p); err != nil {
		return mink.NewErrorResult(err), err
	}

	// Process projections (temporary - in production use subscriptions)
	h.processProjections(ctx, p.StreamID())

	return mink.NewSuccessResult(p.AggregateID(), p.Version()), nil
}

func (h *ProductHandler) processProjections(ctx context.Context, streamID string) {
	events, _ := h.store.LoadRaw(ctx, streamID, 0)
	// This would be injected in production
	// For now, projections are processed by the application
}
```

---

## What's Next?

You've built the query side of CQRS with:

- ✅ Product catalog read model
- ✅ Order history read model  
- ✅ Real-time dashboard projection
- ✅ Query services with filters
- ✅ Inline and live projections

In **Part 5: Testing**, you'll:

- Write BDD tests for aggregates
- Test projections with fixtures
- Add integration tests
- Use assertion helpers

<div class="code-example" markdown="1">

**Next**: [Part 5: Testing →](/tutorial/05-testing)

</div>

---

## Key Takeaways

| Concept | Description |
|---------|-------------|
| **Projection** | Transforms events into optimized read models |
| **Inline** | Synchronous, strong consistency |
| **Async** | Background processing, eventual consistency |
| **Live** | Real-time updates, transient |
| **Checkpoint** | Tracks last processed position |

---

{: .highlight }
> 💡 **Best Practice**: Keep projections simple and focused. One projection per read model. If a query needs data from multiple sources, create a composite projection or join at query time.
