package mink

import (
	"context"
	"errors"
	"sync"
)

// Repository errors
var (
	// ErrNotFound indicates the requested entity was not found.
	ErrNotFound = errors.New("mink: not found")

	// ErrAlreadyExists indicates the entity already exists.
	ErrAlreadyExists = errors.New("mink: already exists")

	// ErrInvalidQuery indicates the query is invalid.
	ErrInvalidQuery = errors.New("mink: invalid query")
)

// ReadModelRepository provides generic CRUD operations for read models.
// T is the read model type.
type ReadModelRepository[T any] interface {
	// Get retrieves a read model by ID.
	// Returns ErrNotFound if not found.
	Get(ctx context.Context, id string) (*T, error)

	// GetMany retrieves multiple read models by their IDs.
	// Missing IDs are silently skipped.
	GetMany(ctx context.Context, ids []string) ([]*T, error)

	// Find queries read models with the given criteria.
	Find(ctx context.Context, query Query) ([]*T, error)

	// FindOne returns the first read model matching the query.
	// Returns ErrNotFound if no match.
	FindOne(ctx context.Context, query Query) (*T, error)

	// Count returns the number of read models matching the query.
	Count(ctx context.Context, query Query) (int64, error)

	// Insert creates a new read model.
	// Returns ErrAlreadyExists if ID already exists.
	Insert(ctx context.Context, model *T) error

	// Update modifies an existing read model.
	// Returns ErrNotFound if not found.
	Update(ctx context.Context, id string, updateFn func(*T)) error

	// Upsert creates or updates a read model.
	Upsert(ctx context.Context, model *T) error

	// Delete removes a read model by ID.
	// Returns ErrNotFound if not found.
	Delete(ctx context.Context, id string) error

	// DeleteMany removes all read models matching the query.
	// Returns the number of deleted models.
	DeleteMany(ctx context.Context, query Query) (int64, error)

	// Clear removes all read models.
	Clear(ctx context.Context) error
}

// Query represents a query for read models.
type Query struct {
	// Filters to apply.
	Filters []Filter

	// Ordering criteria.
	OrderBy []OrderBy

	// Maximum number of results to return.
	// 0 means no limit.
	Limit int

	// Number of results to skip.
	Offset int

	// IncludeCount includes the total count in paginated results.
	IncludeCount bool
}

// NewQuery creates a new empty Query.
func NewQuery() *Query {
	return &Query{}
}

// Where adds a filter condition.
func (q *Query) Where(field string, op FilterOp, value interface{}) *Query {
	q.Filters = append(q.Filters, Filter{
		Field: field,
		Op:    op,
		Value: value,
	})
	return q
}

// And is an alias for Where for readability.
func (q *Query) And(field string, op FilterOp, value interface{}) *Query {
	return q.Where(field, op, value)
}

// OrderByAsc adds ascending order.
func (q *Query) OrderByAsc(field string) *Query {
	q.OrderBy = append(q.OrderBy, OrderBy{
		Field: field,
		Desc:  false,
	})
	return q
}

// OrderByDesc adds descending order.
func (q *Query) OrderByDesc(field string) *Query {
	q.OrderBy = append(q.OrderBy, OrderBy{
		Field: field,
		Desc:  true,
	})
	return q
}

// WithLimit sets the maximum number of results.
func (q *Query) WithLimit(limit int) *Query {
	q.Limit = limit
	return q
}

// WithOffset sets the number of results to skip.
func (q *Query) WithOffset(offset int) *Query {
	q.Offset = offset
	return q
}

// WithPagination sets limit and offset for pagination.
func (q *Query) WithPagination(page, pageSize int) *Query {
	q.Limit = pageSize
	q.Offset = (page - 1) * pageSize
	if q.Offset < 0 {
		q.Offset = 0
	}
	return q
}

// WithCount includes total count in results.
func (q *Query) WithCount() *Query {
	q.IncludeCount = true
	return q
}

// Build returns a copy of the query (useful for chaining).
func (q *Query) Build() Query {
	return *q
}

// Filter represents a query filter condition.
type Filter struct {
	// Field is the field name to filter on.
	Field string

	// Op is the comparison operator.
	Op FilterOp

	// Value is the value to compare against.
	Value interface{}
}

// FilterOp represents a filter operation.
type FilterOp string

const (
	// FilterOpEq matches equal values.
	FilterOpEq FilterOp = "="

	// FilterOpNe matches not equal values.
	FilterOpNe FilterOp = "!="

	// FilterOpGt matches greater than values.
	FilterOpGt FilterOp = ">"

	// FilterOpGte matches greater than or equal values.
	FilterOpGte FilterOp = ">="

	// FilterOpLt matches less than values.
	FilterOpLt FilterOp = "<"

	// FilterOpLte matches less than or equal values.
	FilterOpLte FilterOp = "<="

	// FilterOpIn matches any value in a list.
	FilterOpIn FilterOp = "IN"

	// FilterOpNotIn matches no value in a list.
	FilterOpNotIn FilterOp = "NOT IN"

	// FilterOpLike matches using SQL LIKE pattern.
	FilterOpLike FilterOp = "LIKE"

	// FilterOpIsNull matches null values.
	FilterOpIsNull FilterOp = "IS NULL"

	// FilterOpIsNotNull matches non-null values.
	FilterOpIsNotNull FilterOp = "IS NOT NULL"

	// FilterOpContains matches arrays containing a value.
	FilterOpContains FilterOp = "CONTAINS"

	// FilterOpBetween matches values between two bounds.
	FilterOpBetween FilterOp = "BETWEEN"
)

// OrderBy represents a sort order.
type OrderBy struct {
	// Field is the field name to sort by.
	Field string

	// Desc specifies descending order.
	Desc bool
}

// QueryResult contains query results with optional count.
type QueryResult[T any] struct {
	// Items contains the matching read models.
	Items []*T

	// TotalCount is the total number of matching items (before pagination).
	// Only populated if IncludeCount was true in the query.
	TotalCount int64

	// HasMore indicates if there are more results beyond the limit.
	HasMore bool
}

// ReadModelID is an interface for read models that can return their ID.
type ReadModelID interface {
	// GetID returns the read model's unique identifier.
	GetID() string
}

// InMemoryRepository provides an in-memory implementation of ReadModelRepository.
// Useful for testing and prototyping.
type InMemoryRepository[T any] struct {
	data  map[string]*T
	mu    sync.RWMutex
	getID func(*T) string
}

// NewInMemoryRepository creates a new in-memory repository.
// The getID function extracts the ID from a read model.
func NewInMemoryRepository[T any](getID func(*T) string) *InMemoryRepository[T] {
	return &InMemoryRepository[T]{
		data:  make(map[string]*T),
		getID: getID,
	}
}

// Get retrieves a read model by ID.
func (r *InMemoryRepository[T]) Get(ctx context.Context, id string) (*T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if model, ok := r.data[id]; ok {
		return model, nil
	}
	return nil, ErrNotFound
}

// GetMany retrieves multiple read models by their IDs.
func (r *InMemoryRepository[T]) GetMany(ctx context.Context, ids []string) ([]*T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var results []*T
	for _, id := range ids {
		if model, ok := r.data[id]; ok {
			results = append(results, model)
		}
	}
	return results, nil
}

// Find queries read models with the given criteria.
// Note: This is a basic implementation that doesn't support all filter operations.
func (r *InMemoryRepository[T]) Find(ctx context.Context, query Query) ([]*T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var results []*T
	for _, model := range r.data {
		results = append(results, model)
	}

	// Apply limit and offset
	if query.Offset > 0 && query.Offset < len(results) {
		results = results[query.Offset:]
	} else if query.Offset >= len(results) {
		return []*T{}, nil
	}

	if query.Limit > 0 && query.Limit < len(results) {
		results = results[:query.Limit]
	}

	return results, nil
}

// FindOne returns the first read model matching the query.
func (r *InMemoryRepository[T]) FindOne(ctx context.Context, query Query) (*T, error) {
	query.Limit = 1
	results, err := r.Find(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, ErrNotFound
	}
	return results[0], nil
}

// Count returns the number of read models matching the query.
// Note: This basic implementation ignores query filters and returns total count.
// For production use with filtering, implement a database-backed repository.
func (r *InMemoryRepository[T]) Count(ctx context.Context, query Query) (int64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// If no filters, return total count
	if len(query.Filters) == 0 {
		return int64(len(r.data)), nil
	}

	// With filters, count would need reflection or type-specific implementation
	// For testing purposes, return total count with a note
	return int64(len(r.data)), nil
}

// Insert creates a new read model.
func (r *InMemoryRepository[T]) Insert(ctx context.Context, model *T) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := r.getID(model)
	if _, exists := r.data[id]; exists {
		return ErrAlreadyExists
	}

	r.data[id] = model
	return nil
}

// Update modifies an existing read model.
func (r *InMemoryRepository[T]) Update(ctx context.Context, id string, updateFn func(*T)) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	model, ok := r.data[id]
	if !ok {
		return ErrNotFound
	}

	updateFn(model)
	return nil
}

// Upsert creates or updates a read model.
func (r *InMemoryRepository[T]) Upsert(ctx context.Context, model *T) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := r.getID(model)
	r.data[id] = model
	return nil
}

// Delete removes a read model by ID.
func (r *InMemoryRepository[T]) Delete(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.data[id]; !exists {
		return ErrNotFound
	}

	delete(r.data, id)
	return nil
}

// DeleteMany removes all read models matching the query.
// Note: This basic implementation ignores query filters and deletes all items
// when filters are provided. For production use with filtering, implement
// a database-backed repository.
func (r *InMemoryRepository[T]) DeleteMany(ctx context.Context, query Query) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// If no filters, delete all (same as Clear)
	count := int64(len(r.data))
	r.data = make(map[string]*T)
	return count, nil
}

// Clear removes all read models.
func (r *InMemoryRepository[T]) Clear(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.data = make(map[string]*T)
	return nil
}

// Len returns the number of items in the repository.
func (r *InMemoryRepository[T]) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.data)
}

// Exists checks if a read model with the given ID exists.
func (r *InMemoryRepository[T]) Exists(ctx context.Context, id string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, exists := r.data[id]
	return exists, nil
}

// GetAll returns all read models in the repository.
func (r *InMemoryRepository[T]) GetAll(ctx context.Context) ([]*T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	results := make([]*T, 0, len(r.data))
	for _, model := range r.data {
		results = append(results, model)
	}
	return results, nil
}
