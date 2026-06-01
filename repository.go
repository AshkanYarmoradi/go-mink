package mink

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
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

	// FilterOpContains matches values that contain the given value.
	// On JSONB columns it tests containment (the stored array or object
	// contains the value); on text columns it performs a case-sensitive
	// substring match with the value treated literally.
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

// NewInMemoryRepositoryFor creates a new in-memory repository for a read model
// whose pointer type implements ReadModelID. The ID is derived from the model's
// GetID method, so no explicit getID function is required.
//
//	repo := NewInMemoryRepositoryFor[Account]() // *Account must implement ReadModelID
func NewInMemoryRepositoryFor[T any]() *InMemoryRepository[T] {
	return NewInMemoryRepository(func(m *T) string {
		if r, ok := any(m).(ReadModelID); ok {
			return r.GetID()
		}
		return ""
	})
}

// Get retrieves a read model by ID.
// The returned pointer is a shallow copy of the stored model, so mutating it
// does not affect repository state (use Update for that).
func (r *InMemoryRepository[T]) Get(ctx context.Context, id string) (*T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if model, ok := r.data[id]; ok {
		return copyModel(model), nil
	}
	return nil, ErrNotFound
}

// GetMany retrieves multiple read models by their IDs.
// Returned pointers are shallow copies of the stored models.
func (r *InMemoryRepository[T]) GetMany(ctx context.Context, ids []string) ([]*T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var results []*T
	for _, id := range ids {
		if model, ok := r.data[id]; ok {
			results = append(results, copyModel(model))
		}
	}
	return results, nil
}

// Find queries read models with the given criteria. Filters (AND-combined),
// ordering, offset, and limit are all applied. When no OrderBy is supplied the
// results are ordered by ID for deterministic pagination. Returned pointers are
// shallow copies of the stored models.
func (r *InMemoryRepository[T]) Find(ctx context.Context, query Query) ([]*T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	matched, err := r.matchingModels(query)
	if err != nil {
		return nil, err
	}

	r.orderModels(matched, query.OrderBy)

	// Apply offset and limit.
	if query.Offset > 0 {
		if query.Offset >= len(matched) {
			return []*T{}, nil
		}
		matched = matched[query.Offset:]
	}
	if query.Limit > 0 && query.Limit < len(matched) {
		matched = matched[:query.Limit]
	}

	results := make([]*T, len(matched))
	for i, m := range matched {
		results[i] = copyModel(m)
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

// Count returns the number of read models matching the query's filters.
func (r *InMemoryRepository[T]) Count(ctx context.Context, query Query) (int64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(query.Filters) == 0 {
		return int64(len(r.data)), nil
	}

	matched, err := r.matchingModels(query)
	if err != nil {
		return 0, err
	}
	return int64(len(matched)), nil
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

// DeleteMany removes all read models matching the query's filters and returns
// the number deleted. With no filters it removes everything (equivalent to Clear).
func (r *InMemoryRepository[T]) DeleteMany(ctx context.Context, query Query) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// No filters: delete all (same as Clear).
	if len(query.Filters) == 0 {
		count := int64(len(r.data))
		r.data = make(map[string]*T)
		return count, nil
	}

	compiled, err := compileFilters(query.Filters)
	if err != nil {
		return 0, err
	}
	var toDelete []string
	for id, model := range r.data {
		if matchesCompiled(model, compiled) {
			toDelete = append(toDelete, id)
		}
	}
	for _, id := range toDelete {
		delete(r.data, id)
	}
	return int64(len(toDelete)), nil
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
// Returned pointers are shallow copies of the stored models.
func (r *InMemoryRepository[T]) GetAll(ctx context.Context) ([]*T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	results := make([]*T, 0, len(r.data))
	for _, model := range r.data {
		results = append(results, copyModel(model))
	}
	return results, nil
}

// matchingModels returns the stored models (by reference) that satisfy the
// query's filters. Filters are compiled once (regex/slice/bounds precomputed)
// before the model loop, so per-model evaluation is allocation-light. Callers
// hold at least an RLock.
func (r *InMemoryRepository[T]) matchingModels(query Query) ([]*T, error) {
	compiled, err := compileFilters(query.Filters)
	if err != nil {
		return nil, err
	}
	matched := make([]*T, 0, len(r.data))
	for _, model := range r.data {
		if matchesCompiled(model, compiled) {
			matched = append(matched, model)
		}
	}
	return matched, nil
}

// orderModels sorts models by the given OrderBy clauses, always falling back to
// the repository ID as a final tiebreaker so the order is deterministic (stable
// pagination). Sort keys are extracted once per model (decorate-sort-undecorate)
// instead of via reflection on every comparison.
func (r *InMemoryRepository[T]) orderModels(models []*T, orderBy []OrderBy) {
	if len(orderBy) == 0 {
		sort.SliceStable(models, func(i, j int) bool {
			return r.getID(models[i]) < r.getID(models[j])
		})
		return
	}

	type decorated struct {
		model *T
		keys  []interface{}
		id    string
	}
	dec := make([]decorated, len(models))
	for i, m := range models {
		keys := make([]interface{}, len(orderBy))
		for k, ob := range orderBy {
			keys[k], _ = fieldValue(m, ob.Field)
		}
		dec[i] = decorated{model: m, keys: keys, id: r.getID(m)}
	}

	sort.SliceStable(dec, func(i, j int) bool {
		for k, ob := range orderBy {
			cmp, ok := compareOrdered(dec[i].keys[k], dec[j].keys[k])
			if !ok || cmp == 0 {
				continue
			}
			if ob.Desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return dec[i].id < dec[j].id // deterministic tiebreaker
	})

	for i := range dec {
		models[i] = dec[i].model
	}
}

// copyModel returns a shallow copy of a stored model so callers cannot mutate
// repository state by holding the returned pointer.
func copyModel[T any](m *T) *T {
	if m == nil {
		return nil
	}
	cp := *m
	return &cp
}

// compiledFilter is a query Filter with its expensive parts precomputed once per
// query (the regexp for LIKE, the value slice for IN/NOT IN, the bounds for
// BETWEEN), so per-model evaluation does no compilation.
type compiledFilter struct {
	field string
	op    FilterOp
	value interface{}
	re    *regexp.Regexp // FilterOpLike
	set   []interface{}  // FilterOpIn / FilterOpNotIn
	lo    interface{}    // FilterOpBetween low bound
	hi    interface{}    // FilterOpBetween high bound
}

// compileFilters validates and precompiles the query filters once. It returns
// ErrInvalidQuery for structurally-invalid filters (non-string LIKE, non-slice
// or empty IN/NOT IN list, malformed BETWEEN, unknown operator) so the in-memory
// repository fails the same way the PostgreSQL repository does.
func compileFilters(filters []Filter) ([]compiledFilter, error) {
	compiled := make([]compiledFilter, len(filters))
	for i, f := range filters {
		cf := compiledFilter{field: f.Field, op: f.Op, value: f.Value}
		switch f.Op {
		case FilterOpEq, FilterOpNe, FilterOpGt, FilterOpGte, FilterOpLt, FilterOpLte,
			FilterOpContains, FilterOpIsNull, FilterOpIsNotNull:
			// no precomputation required
		case FilterOpLike:
			pattern, ok := f.Value.(string)
			if !ok {
				return nil, fmt.Errorf("%w: LIKE requires a string value", ErrInvalidQuery)
			}
			re, err := compileLike(pattern)
			if err != nil {
				return nil, err
			}
			cf.re = re
		case FilterOpIn, FilterOpNotIn:
			items, err := toSlice(f.Value)
			if err != nil {
				return nil, fmt.Errorf("%w: %s requires a slice value", ErrInvalidQuery, f.Op)
			}
			if len(items) == 0 {
				return nil, fmt.Errorf("%w: %s requires a non-empty slice", ErrInvalidQuery, f.Op)
			}
			cf.set = items
		case FilterOpBetween:
			items, err := toSlice(f.Value)
			if err != nil || len(items) != 2 {
				return nil, fmt.Errorf("%w: BETWEEN requires a [low, high] value", ErrInvalidQuery)
			}
			cf.lo, cf.hi = items[0], items[1]
		default:
			return nil, fmt.Errorf("%w: unsupported filter operator %q", ErrInvalidQuery, f.Op)
		}
		compiled[i] = cf
	}
	return compiled, nil
}

// matchesCompiled reports whether a model satisfies all compiled filters (AND).
func matchesCompiled[T any](model *T, filters []compiledFilter) bool {
	for i := range filters {
		if !matchCompiled(model, filters[i]) {
			return false
		}
	}
	return true
}

// matchCompiled evaluates a single precompiled filter against a model.
func matchCompiled[T any](model *T, cf compiledFilter) bool {
	val, found := fieldValue(model, cf.field)

	switch cf.op {
	case FilterOpIsNull:
		return !found || isNilValue(val)
	case FilterOpIsNotNull:
		return found && !isNilValue(val)
	}

	if !found {
		// Missing field: only "not equal" is satisfiable.
		return cf.op == FilterOpNe
	}

	switch cf.op {
	case FilterOpEq:
		return valuesEqual(val, cf.value)
	case FilterOpNe:
		return !valuesEqual(val, cf.value)
	case FilterOpGt, FilterOpGte, FilterOpLt, FilterOpLte:
		cmp, ok := compareOrdered(val, cf.value)
		if !ok {
			return false
		}
		switch cf.op {
		case FilterOpGt:
			return cmp > 0
		case FilterOpGte:
			return cmp >= 0
		case FilterOpLt:
			return cmp < 0
		default: // FilterOpLte
			return cmp <= 0
		}
	case FilterOpIn, FilterOpNotIn:
		in := false
		for _, item := range cf.set {
			if valuesEqual(val, item) {
				in = true
				break
			}
		}
		if cf.op == FilterOpIn {
			return in
		}
		return !in
	case FilterOpLike:
		s, ok := val.(string)
		if !ok {
			return false
		}
		return cf.re.MatchString(s)
	case FilterOpContains:
		ok, _ := matchContains(val, cf.value)
		return ok
	case FilterOpBetween:
		lo, lok := compareOrdered(val, cf.lo)
		hi, hok := compareOrdered(val, cf.hi)
		if !lok || !hok {
			return false
		}
		return lo >= 0 && hi <= 0
	default:
		return false
	}
}

// fieldIndexCache memoizes the field-name → index-path map per struct type so
// fieldValue does not re-walk struct tags/fields on every access.
var fieldIndexCache sync.Map // map[reflect.Type]map[string][]int

func fieldIndexFor(t reflect.Type) map[string][]int {
	if cached, ok := fieldIndexCache.Load(t); ok {
		return cached.(map[string][]int)
	}
	m := make(map[string][]int)
	buildFieldIndex(t, m)
	fieldIndexCache.Store(t, m)
	return m
}

// buildFieldIndex populates m with lookup keys (JSON tag and lower-cased field
// name) → field index path, including promoted fields from embedded (anonymous)
// structs. It walks the embedding tree breadth-first by depth so that a
// shallower field always wins over a more deeply-embedded field with the same
// key, mirroring Go/JSON field promotion (the outer field shadows the embedded
// one). Among fields at the same depth, earlier declaration order wins.
func buildFieldIndex(root reflect.Type, m map[string][]int) {
	type node struct {
		t      reflect.Type
		prefix []int
	}
	level := []node{{t: root}}
	for len(level) > 0 {
		var nextLevel []node
		for _, n := range level {
			for i := 0; i < n.t.NumField(); i++ {
				sf := n.t.Field(i)
				idx := make([]int, len(n.prefix)+1)
				copy(idx, n.prefix)
				idx[len(n.prefix)] = i

				if sf.Anonymous {
					ft := sf.Type
					for ft.Kind() == reflect.Pointer {
						ft = ft.Elem()
					}
					if ft.Kind() == reflect.Struct {
						// Descend into the embedded struct on the next (deeper) pass.
						nextLevel = append(nextLevel, node{t: ft, prefix: idx})
						continue
					}
				}
				if sf.PkgPath != "" {
					continue // unexported
				}
				if tag := strings.Split(sf.Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
					if _, exists := m[tag]; !exists {
						m[tag] = idx
					}
				}
				if lname := strings.ToLower(sf.Name); lname != "" {
					if _, exists := m[lname]; !exists {
						m[lname] = idx
					}
				}
			}
		}
		level = nextLevel
	}
}

// fieldValue extracts a field value from a struct by name, matching the JSON tag
// first and then the Go field name case-insensitively, including promoted fields
// from embedded structs. The model may be a pointer.
func fieldValue(model interface{}, field string) (interface{}, bool) {
	v := reflect.ValueOf(model)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, false
	}

	idxMap := fieldIndexFor(v.Type())
	idx, ok := idxMap[field]
	if !ok {
		idx, ok = idxMap[strings.ToLower(field)]
	}
	if !ok {
		return nil, false
	}
	return fieldByIndexPath(v, idx)
}

// fieldByIndexPath follows an index path (descending through embedded structs),
// returning false if a nil embedded pointer is encountered along the way.
func fieldByIndexPath(v reflect.Value, idx []int) (interface{}, bool) {
	for _, i := range idx {
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil, false
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v.Interface(), true
}

// isNilValue reports whether a reflected value is nil (pointer, interface, map, slice).
func isNilValue(val interface{}) bool {
	if val == nil {
		return true
	}
	rv := reflect.ValueOf(val)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return rv.IsNil()
	default:
		return false
	}
}

// valuesEqual compares two values for equality, normalizing numeric types.
func valuesEqual(a, b interface{}) bool {
	if cmp, ok := compareOrdered(a, b); ok {
		return cmp == 0
	}
	return reflect.DeepEqual(a, b)
}

// compareOrdered compares two values, returning -1/0/1 and whether the
// comparison is defined. Numbers (cross int/uint/float), strings, and time.Time
// are supported.
func compareOrdered(a, b interface{}) (int, bool) {
	if at, ok := a.(time.Time); ok {
		if bt, ok := toTime(b); ok {
			switch {
			case at.Before(bt):
				return -1, true
			case at.After(bt):
				return 1, true
			default:
				return 0, true
			}
		}
		return 0, false
	}

	if af, ok := toFloat(a); ok {
		if bf, ok := toFloat(b); ok {
			switch {
			case af < bf:
				return -1, true
			case af > bf:
				return 1, true
			default:
				return 0, true
			}
		}
		return 0, false
	}

	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		return strings.Compare(as, bs), true
	}

	return 0, false
}

// toFloat converts a numeric value to float64.
func toFloat(v interface{}) (float64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	default:
		return 0, false
	}
}

// toTime converts a value to time.Time if possible.
func toTime(v interface{}) (time.Time, bool) {
	if t, ok := v.(time.Time); ok {
		return t, true
	}
	return time.Time{}, false
}

// toSlice converts an interface holding a slice/array into a []interface{}.
func toSlice(v interface{}) ([]interface{}, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, fmt.Errorf("value of kind %s is not a slice", rv.Kind())
	}
	out := make([]interface{}, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out[i] = rv.Index(i).Interface()
	}
	return out, nil
}

// matchContains tests substring (for strings) or membership (for slices).
func matchContains(val, target interface{}) (bool, error) {
	if s, ok := val.(string); ok {
		if sub, ok := target.(string); ok {
			return strings.Contains(s, sub), nil
		}
		return false, nil
	}
	if items, err := toSlice(val); err == nil {
		for _, item := range items {
			if valuesEqual(item, target) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, nil
}

// compileLike compiles a SQL LIKE pattern (% = any sequence, _ = any single
// char) into an anchored, case-sensitive regexp. The (?s) flag makes `.` match
// newlines, matching SQL LIKE semantics on multi-line values.
func compileLike(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?s)^")
	for _, r := range pattern {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("%w: invalid LIKE pattern: %v", ErrInvalidQuery, err)
	}
	return re, nil
}
