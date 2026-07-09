// Package postgres provides a PostgreSQL implementation of the read model repository.
package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	mink "go-mink.dev"
)

// dbExecutor abstracts database operations for both *sql.DB and *sql.Tx.
// This interface enables code reuse between PostgresRepository and TxRepository.
type dbExecutor interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

// ColumnType represents SQL column type information.
type ColumnType struct {
	Name       string
	SQLType    string
	Nullable   bool
	PrimaryKey bool
	Index      bool
	Unique     bool
	Default    string
}

// TableSchema represents the schema of a read model table.
type TableSchema struct {
	TableName string
	Schema    string
	Columns   []ColumnType
	Indexes   []IndexDef
}

// IndexDef represents an index definition.
type IndexDef struct {
	Name    string
	Columns []string
	Unique  bool
}

// ReadModelOption configures the PostgresRepository.
type ReadModelOption func(*readModelConfig)

type readModelConfig struct {
	schema      string
	tableName   string
	idField     string
	autoMigrate bool
}

// WithReadModelSchema sets the PostgreSQL schema for the read model table.
func WithReadModelSchema(schema string) ReadModelOption {
	return func(c *readModelConfig) {
		c.schema = schema
	}
}

// WithTableName sets the table name for the read model.
func WithTableName(name string) ReadModelOption {
	return func(c *readModelConfig) {
		c.tableName = name
	}
}

// WithIDField sets the field name used as the primary key.
// Default is "ID".
func WithIDField(field string) ReadModelOption {
	return func(c *readModelConfig) {
		c.idField = field
	}
}

// WithAutoMigrate enables automatic table creation and migration.
// Default is true.
func WithAutoMigrate(enabled bool) ReadModelOption {
	return func(c *readModelConfig) {
		c.autoMigrate = enabled
	}
}

// PostgresRepository provides a PostgreSQL implementation of ReadModelRepository.
// It supports automatic schema migration based on struct tags.
type PostgresRepository[T any] struct {
	db          *sql.DB
	config      readModelConfig
	tableSchema *TableSchema
	columns     []string
	idIndex     int
	// scanKinds and columnField are parallel to columns and precomputed once at
	// construction (see buildScanPlan), so the per-row scan path does no field
	// enumeration or column-name mapping — only indexed lookups.
	//
	// scanKinds[i] records how column i's NULL is coalesced on read: it is
	// non-scanKindNone only when the column is declared `nullable` AND its
	// destination field is a non-pointer scalar kind — the exact case
	// database/sql cannot scan a NULL into. Every other column stays
	// scanKindNone and is scanned straight into the field address, unchanged.
	//
	// columnField[i] is the struct field index feeding column i, or -1 when no
	// exported field maps to it (the column then gets a discard destination).
	scanKinds   []scalarScanKind
	columnField []int
	// modelType is the resolved (pointer-dereferenced) struct type of T, set
	// once by buildTableSchema after it validates T is a struct. buildScanPlan
	// and fieldForColumn reuse it instead of re-deriving and re-validating the
	// type, so the struct-type resolution lives in exactly one place.
	modelType reflect.Type
}

// scalarScanKind identifies how a NULL is coalesced when read into a nullable
// scalar column's destination field: the stored NULL becomes the field's Go
// zero value instead of failing rows.Scan. time.Time is distinguished from
// other structs (which map to JSONB) so only real scalar destinations wrap.
type scalarScanKind uint8

const (
	scanKindNone   scalarScanKind = iota // not a nullable scalar; scan directly
	scanKindString                       // string
	scanKindBool                         // bool
	scanKindInt                          // Int, Int8..Int64
	scanKindUint                         // Uint, Uint8..Uint64 (read through int64)
	scanKindFloat                        // Float32, Float64
	scanKindTime                         // time.Time
)

// scalarKindForType returns the NULL-coalescing scan kind for a non-pointer
// scalar type, or scanKindNone for anything else — pointers (already NULL-safe
// to nil), []byte/slices/maps and non-time structs (scanned as BYTEA/JSONB,
// also NULL-safe to nil). Only these scalar kinds fail a direct NULL scan.
func scalarKindForType(t reflect.Type) scalarScanKind {
	switch t.Kind() {
	case reflect.String:
		return scanKindString
	case reflect.Bool:
		return scanKindBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return scanKindInt
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return scanKindUint
	case reflect.Float32, reflect.Float64:
		return scanKindFloat
	case reflect.Struct:
		if t == reflect.TypeOf(time.Time{}) {
			return scanKindTime
		}
	}
	return scanKindNone
}

// NewPostgresRepository creates a new PostgreSQL-backed repository for read models.
// The type T should be a struct with mink tags for column mapping.
//
// Supported struct tags:
//   - `mink:"column_name"` - Column name (default: snake_case of field name)
//   - `mink:"-"` - Skip this field
//   - `mink:"column_name,pk"` - Primary key
//   - `mink:"column_name,index"` - Create index
//   - `mink:"column_name,unique"` - Unique constraint
//   - `mink:"column_name,nullable"` - Allow NULL values (see nullability note below)
//   - `mink:"column_name,default=value"` - Default value
//
// Nullability: `nullable` governs both DDL and reads. On the write side the
// column is emitted without NOT NULL. On the read side, a stored NULL in a
// nullable non-pointer scalar column (string, the int/uint kinds, float, bool,
// time.Time) is coalesced to the field's Go zero value ("", 0, false, zero
// time) instead of failing the scan — so a single NULL never aborts a Find/Get.
// The write path is unaffected: persisting a zero-value scalar stores that zero
// value, never NULL. To persist and read back a distinguishable NULL, use a
// pointer field (*string, …), which reads NULL as nil. A NULL in a column that
// is NOT tagged nullable surfaces a typed *mink.NullColumnError. A non-NULL
// value that does not fit a nullable numeric field (e.g. a value beyond int8,
// or a negative value in an unsigned field) surfaces a typed
// *mink.ColumnValueRangeError rather than silently truncating.
//
// Example:
//
//	type OrderSummary struct {
//	    OrderID    string    `mink:"order_id,pk"`
//	    CustomerID string    `mink:"customer_id,index"`
//	    Status     string    `mink:"status"`
//	    Total      float64   `mink:"total_amount"`
//	    CreatedAt  time.Time `mink:"created_at"`
//	}
//
//	repo, err := postgres.NewPostgresRepository[OrderSummary](db,
//	    postgres.WithReadModelSchema("projections"),
//	    postgres.WithTableName("order_summaries"),
//	)
//
// Auto-migration caveat: by default (WithAutoMigrate is true) this constructor
// runs blocking schema DDL — CREATE SCHEMA/TABLE/INDEX and ALTER TABLE — using
// context.Background(), so it cannot be cancelled or deadline-bounded by the
// caller. Production callers that need a cancellable/bounded migration should
// either use NewPostgresRepositoryContext to thread a context through, or
// disable auto-migration with WithAutoMigrate(false) and call Migrate(ctx)
// explicitly with their own context.
func NewPostgresRepository[T any](db *sql.DB, opts ...ReadModelOption) (*PostgresRepository[T], error) {
	return NewPostgresRepositoryContext[T](context.Background(), db, opts...)
}

// NewPostgresRepositoryContext is like NewPostgresRepository but threads the
// provided context into the auto-migration step (when WithAutoMigrate is
// enabled, the default). This lets callers bound or cancel the blocking schema
// DDL that runs at construction. The context is only used for migration; once
// the repository is returned, per-operation methods take their own context.
func NewPostgresRepositoryContext[T any](ctx context.Context, db *sql.DB, opts ...ReadModelOption) (*PostgresRepository[T], error) {
	config := readModelConfig{
		schema:      "public",
		idField:     "ID",
		autoMigrate: true,
	}

	for _, opt := range opts {
		opt(&config)
	}

	// Infer table name from type if not specified
	if config.tableName == "" {
		var t T
		typ := reflect.TypeOf(t)
		if typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		config.tableName = toSnakeCase(typ.Name())
	}

	// Validate identifiers
	if err := validateSchemaName(config.schema); err != nil {
		return nil, err
	}
	if err := validateIdentifier(config.tableName, "table"); err != nil {
		return nil, err
	}

	repo := &PostgresRepository[T]{
		db:     db,
		config: config,
	}

	// Build table schema from struct
	schema, err := repo.buildTableSchema()
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/readmodel: failed to build schema: %w", err)
	}
	repo.tableSchema = schema

	// Build column list
	repo.columns = make([]string, len(schema.Columns))
	for i, col := range schema.Columns {
		repo.columns[i] = col.Name
		if col.PrimaryKey {
			repo.idIndex = i
		}
	}

	// Precompute the per-column read plan (field mapping + NULL-coalescing
	// kinds), so the per-row scan path is indexed lookups rather than repeated
	// reflection and column-name mapping.
	repo.buildScanPlan()

	// Auto-migrate if enabled
	if config.autoMigrate {
		if ctx == nil {
			ctx = context.Background()
		}
		if err := repo.Migrate(ctx); err != nil {
			return nil, fmt.Errorf("mink/postgres/readmodel: migration failed: %w", err)
		}
	}

	return repo, nil
}

// buildTableSchema analyzes the struct type and builds a table schema.
func (r *PostgresRepository[T]) buildTableSchema() (*TableSchema, error) {
	var t T
	typ := reflect.TypeOf(t)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("type must be a struct, got %s", typ.Kind())
	}
	// Record the resolved struct type so buildScanPlan / fieldForColumn reuse it
	// rather than re-deriving and re-validating T.
	r.modelType = typ

	schema := &TableSchema{
		TableName: r.config.tableName,
		Schema:    r.config.schema,
	}

	hasPK := false
	idFieldIdx := -1 // Track the ID field index for fallback PK

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		col, skip := r.parseFieldTag(field)
		if skip {
			continue
		}

		if col.PrimaryKey {
			hasPK = true
		}

		// Track the ID field (but don't mark as PK yet - wait to see if explicit pk exists)
		if field.Name == r.config.idField && idFieldIdx == -1 {
			idFieldIdx = len(schema.Columns)
		}

		schema.Columns = append(schema.Columns, col)

		// Create index definition if needed
		if col.Index && !col.PrimaryKey {
			schema.Indexes = append(schema.Indexes, IndexDef{
				Name:    fmt.Sprintf("idx_%s_%s", schema.TableName, col.Name),
				Columns: []string{col.Name},
				Unique:  col.Unique,
			})
		}
	}

	// Apply fallback PK logic AFTER processing all fields
	if !hasPK {
		if idFieldIdx >= 0 {
			// Use the ID field as PK
			schema.Columns[idFieldIdx].PrimaryKey = true
		} else if len(schema.Columns) > 0 {
			// Use first column as PK
			schema.Columns[0].PrimaryKey = true
		}
	}

	return schema, nil
}

// parseFieldTag parses struct field tags and returns column configuration.
func (r *PostgresRepository[T]) parseFieldTag(field reflect.StructField) (ColumnType, bool) {
	col := ColumnType{
		Name:     toSnakeCase(field.Name),
		SQLType:  goTypeToSQL(field.Type),
		Nullable: false,
	}

	tag := field.Tag.Get("mink")
	if tag == "-" {
		return col, true // skip
	}

	if tag != "" {
		parts := strings.Split(tag, ",")
		if parts[0] != "" {
			col.Name = parts[0]
		}

		for _, part := range parts[1:] {
			switch {
			case part == "pk":
				col.PrimaryKey = true
			case part == "index":
				col.Index = true
			case part == "unique":
				col.Unique = true
				col.Index = true
			case part == "nullable":
				col.Nullable = true
			case strings.HasPrefix(part, "default="):
				defVal := strings.TrimPrefix(part, "default=")
				// Note: Default values are validated during migration, not here,
				// because buildTableSchema doesn't return errors for individual tags.
				col.Default = defVal
			case strings.HasPrefix(part, "type="):
				typeVal := strings.TrimPrefix(part, "type=")
				// Note: Type values are validated during migration, not here.
				col.SQLType = typeVal
			}
		}
	}

	return col, false
}

// Migrate creates or updates the table schema.
func (r *PostgresRepository[T]) Migrate(ctx context.Context) error {
	// Create schema if not exists
	schemaQ := quoteIdentifier(r.config.schema)
	_, err := r.db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+schemaQ)
	if err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	// Build CREATE TABLE statement
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)

	var columns []string
	var pkColumn string
	for _, col := range r.tableSchema.Columns {
		// Validate SQL type if custom
		if col.SQLType != "" {
			if err := validateSQLLiteral(col.SQLType, "type"); err != nil {
				return err
			}
		}

		colDef := fmt.Sprintf("%s %s", quoteIdentifier(col.Name), col.SQLType)

		if col.PrimaryKey {
			pkColumn = col.Name
			colDef += " PRIMARY KEY"
		} else if !col.Nullable {
			colDef += " NOT NULL"
		}

		if col.Unique && !col.PrimaryKey {
			colDef += " UNIQUE"
		}

		if col.Default != "" {
			// Validate default value to prevent SQL injection in DDL
			if err := validateSQLLiteral(col.Default, "default"); err != nil {
				return err
			}
			colDef += " DEFAULT " + col.Default
		}

		columns = append(columns, colDef)
	}

	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (%s)`,
		tableQ,
		strings.Join(columns, ", "),
	)

	_, err = r.db.ExecContext(ctx, createSQL)
	if err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	// Handle schema evolution - add missing columns BEFORE creating indexes
	// This ensures all indexed columns exist
	err = r.migrateColumns(ctx, pkColumn)
	if err != nil {
		return fmt.Errorf("failed to migrate columns: %w", err)
	}

	// Create indexes (after all columns exist)
	for _, idx := range r.tableSchema.Indexes {
		if err := validateIdentifier(idx.Name, "index"); err != nil {
			return err
		}

		// Build full index name with truncation if needed
		// PostgreSQL has a 63 character limit for identifiers
		fullIdxName := safeIndexName(r.config.schema, idx.Name)

		uniqueStr := ""
		if idx.Unique {
			uniqueStr = "UNIQUE "
		}

		quotedCols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			quotedCols[i] = quoteIdentifier(c)
		}

		// Use the safe index name
		idxName := quoteIdentifier(fullIdxName)
		indexSQL := fmt.Sprintf(`CREATE %sINDEX IF NOT EXISTS %s ON %s (%s)`,
			uniqueStr,
			idxName,
			tableQ,
			strings.Join(quotedCols, ", "),
		)

		_, err = r.db.ExecContext(ctx, indexSQL)
		if err != nil {
			return fmt.Errorf("failed to create index %s: %w", idx.Name, err)
		}
	}

	return nil
}

// migrateColumns adds any missing columns to an existing table.
func (r *PostgresRepository[T]) migrateColumns(ctx context.Context, pkColumn string) error {
	// Get existing columns
	existingCols := make(map[string]bool)
	rows, err := r.db.QueryContext(ctx, `
		SELECT column_name 
		FROM information_schema.columns 
		WHERE table_schema = $1 AND table_name = $2
	`, r.config.schema, r.config.tableName)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var colName string
		if err := rows.Scan(&colName); err != nil {
			return err
		}
		existingCols[colName] = true
	}

	if err := rows.Err(); err != nil {
		return err
	}

	// Add missing columns
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)
	for _, col := range r.tableSchema.Columns {
		if existingCols[col.Name] {
			continue
		}
		if col.PrimaryKey {
			// Can't add PK column to existing table easily
			continue
		}

		// Validate SQL type if custom
		if col.SQLType != "" {
			if err := validateSQLLiteral(col.SQLType, "type"); err != nil {
				return err
			}
		}

		colDef := fmt.Sprintf("%s %s", quoteIdentifier(col.Name), col.SQLType)
		if col.Default != "" {
			// Validate default value to prevent SQL injection
			if err := validateSQLLiteral(col.Default, "default"); err != nil {
				return err
			}
			colDef += " DEFAULT " + col.Default
		}
		if !col.Nullable && col.Default == "" {
			// For NOT NULL without default, use appropriate default
			colDef += " DEFAULT " + defaultForType(col.SQLType)
		}

		alterSQL := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s`,
			tableQ, colDef)

		_, err := r.db.ExecContext(ctx, alterSQL)
		if err != nil {
			return fmt.Errorf("failed to add column %s: %w", col.Name, err)
		}
	}

	return nil
}

// Get retrieves a read model by ID.
func (r *PostgresRepository[T]) Get(ctx context.Context, id string) (*T, error) {
	return r.getWithExecutor(ctx, r.db, id)
}

// GetMany retrieves multiple read models by their IDs.
func (r *PostgresRepository[T]) GetMany(ctx context.Context, ids []string) ([]*T, error) {
	return r.getManyWithExecutor(ctx, r.db, ids)
}

// Find queries read models with the given criteria.
func (r *PostgresRepository[T]) Find(ctx context.Context, query mink.Query) ([]*T, error) {
	return r.findWithExecutor(ctx, r.db, query)
}

// FindOne returns the first read model matching the query.
func (r *PostgresRepository[T]) FindOne(ctx context.Context, query mink.Query) (*T, error) {
	return r.findOneWithExecutor(ctx, r.db, query)
}

// Count returns the number of read models matching the query.
func (r *PostgresRepository[T]) Count(ctx context.Context, query mink.Query) (int64, error) {
	return r.countWithExecutor(ctx, r.db, query)
}

// Insert creates a new read model.
func (r *PostgresRepository[T]) Insert(ctx context.Context, model *T) error {
	return r.insertWithExecutor(ctx, r.db, model)
}

// Update modifies an existing read model.
func (r *PostgresRepository[T]) Update(ctx context.Context, id string, updateFn func(*T)) error {
	return r.updateWithExecutor(ctx, r.db, id, updateFn)
}

// Upsert creates or updates a read model.
func (r *PostgresRepository[T]) Upsert(ctx context.Context, model *T) error {
	return r.upsertWithExecutor(ctx, r.db, model)
}

// Delete removes a read model by ID.
func (r *PostgresRepository[T]) Delete(ctx context.Context, id string) error {
	return r.deleteWithExecutor(ctx, r.db, id)
}

// DeleteMany removes all read models matching the query.
func (r *PostgresRepository[T]) DeleteMany(ctx context.Context, query mink.Query) (int64, error) {
	return r.deleteManyWithExecutor(ctx, r.db, query)
}

// Clear removes all read models.
func (r *PostgresRepository[T]) Clear(ctx context.Context) error {
	return r.clearWithExecutor(ctx, r.db)
}

// Exists checks if a read model with the given ID exists.
func (r *PostgresRepository[T]) Exists(ctx context.Context, id string) (bool, error) {
	return r.existsWithExecutor(ctx, r.db, id)
}

// GetAll returns all read models in the repository.
func (r *PostgresRepository[T]) GetAll(ctx context.Context) ([]*T, error) {
	return r.Find(ctx, mink.Query{})
}

// TableName returns the fully qualified table name.
func (r *PostgresRepository[T]) TableName() string {
	return fmt.Sprintf("%s.%s", r.config.schema, r.config.tableName)
}

// Schema returns the PostgreSQL schema name.
func (r *PostgresRepository[T]) Schema() string {
	return r.config.schema
}

// DropTable removes the read model table (use with caution!).
func (r *PostgresRepository[T]) DropTable(ctx context.Context) error {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)
	_, err := r.db.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s CASCADE`, tableQ))
	return err
}

// buildSelectQuery builds a SELECT query from a mink.Query.
// Returns an error if limit or offset are negative.
func (r *PostgresRepository[T]) buildSelectQuery(query mink.Query) (string, []interface{}, error) {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)

	selectCols := make([]string, len(r.columns))
	for i, col := range r.columns {
		selectCols[i] = quoteIdentifier(col)
	}

	whereClause, args, err := r.buildWhereClause(query.Filters)
	if err != nil {
		return "", nil, err
	}
	orderClause := r.buildOrderClause(query.OrderBy)
	limitClause, err := r.buildLimitClauseWithValidation(query.Limit, query.Offset)
	if err != nil {
		return "", nil, err
	}

	sqlQuery := fmt.Sprintf(`SELECT %s FROM %s%s%s%s`,
		strings.Join(selectCols, ", "),
		tableQ,
		whereClause,
		orderClause,
		limitClause,
	)

	return sqlQuery, args, nil
}

// filterOpToSQL maps filter operators to SQL operators.
var filterOpToSQL = map[mink.FilterOp]string{
	mink.FilterOpEq:   "=",
	mink.FilterOpNe:   "!=",
	mink.FilterOpGt:   ">",
	mink.FilterOpGte:  ">=",
	mink.FilterOpLt:   "<",
	mink.FilterOpLte:  "<=",
	mink.FilterOpLike: "LIKE",
}

// requireNonEmptySlice resolves an IN / NOT IN filter value to a non-empty
// slice of arguments. It returns an error when the value is not a slice or is
// an empty slice: a non-slice would otherwise silently drop the condition
// (yielding an over-broad query), and an empty slice would emit invalid SQL
// (IN () / NOT IN ()). Slice-ness is checked with reflection so that a typed
// nil slice (e.g. []interface{}(nil)) is reported as empty rather than as a
// non-slice. The keyword ("IN" or "NOT IN") is only used for the error message.
func requireNonEmptySlice(f mink.Filter, keyword string) ([]interface{}, error) {
	if reflect.ValueOf(f.Value).Kind() != reflect.Slice {
		return nil, fmt.Errorf("mink/postgres/readmodel: %s filter on field %q requires a slice value, got %T", keyword, f.Field, f.Value)
	}
	if vals := toInterfaceSlice(f.Value); len(vals) > 0 {
		return vals, nil
	}
	return nil, fmt.Errorf("mink/postgres/readmodel: %s filter on field %q requires a non-empty slice", keyword, f.Field)
}

// buildInClause builds an IN or NOT IN clause from a slice of values.
func buildInClause(quotedCol, keyword string, values []interface{}, paramIdx *int, args *[]interface{}) string {
	placeholders := make([]string, len(values))
	for i, v := range values {
		placeholders[i] = fmt.Sprintf("$%d", *paramIdx)
		*args = append(*args, v)
		(*paramIdx)++
	}
	return fmt.Sprintf("%s %s (%s)", quotedCol, keyword, strings.Join(placeholders, ", "))
}

// buildWhereClause builds the WHERE clause from filters.
//
// Every operator defined by mink.FilterOp is handled. A filter whose field
// does not resolve to a known column returns ErrUnknownFilterField, and an
// unrecognized operator also returns an error: a query must never silently ignore
// the caller's intent and return (or delete) rows it was asked to filter out.
func (r *PostgresRepository[T]) buildWhereClause(filters []mink.Filter) (string, []interface{}, error) {
	if len(filters) == 0 {
		return "", nil, nil
	}

	var conditions []string
	var args []interface{}
	paramIdx := 1

	for _, f := range filters {
		colName := r.fieldToColumn(f.Field)
		if colName == "" {
			// A field resolving to no column must NOT be silently skipped: if it were
			// the only filter, the WHERE would be empty and DeleteMany would wipe the
			// whole table (Find/Count would scan everything). Reject it instead.
			return "", nil, &mink.UnknownFilterFieldError{Field: f.Field}
		}
		quotedCol := quoteIdentifier(colName)

		// Handle simple comparison operators
		if op, ok := filterOpToSQL[f.Op]; ok {
			conditions = append(conditions, fmt.Sprintf("%s %s $%d", quotedCol, op, paramIdx))
			args = append(args, f.Value)
			paramIdx++
			continue
		}

		// Handle special operators
		switch f.Op {
		case mink.FilterOpIn:
			vals, err := requireNonEmptySlice(f, "IN")
			if err != nil {
				return "", nil, err
			}
			conditions = append(conditions, buildInClause(quotedCol, "IN", vals, &paramIdx, &args))
		case mink.FilterOpNotIn:
			vals, err := requireNonEmptySlice(f, "NOT IN")
			if err != nil {
				return "", nil, err
			}
			conditions = append(conditions, buildInClause(quotedCol, "NOT IN", vals, &paramIdx, &args))
		case mink.FilterOpIsNull:
			conditions = append(conditions, fmt.Sprintf("%s IS NULL", quotedCol))
		case mink.FilterOpIsNotNull:
			conditions = append(conditions, fmt.Sprintf("%s IS NOT NULL", quotedCol))
		case mink.FilterOpBetween:
			vals := toInterfaceSlice(f.Value)
			if len(vals) != 2 {
				return "", nil, fmt.Errorf("mink/postgres/readmodel: BETWEEN filter on field %q requires exactly 2 bounds, got %d", f.Field, len(vals))
			}
			conditions = append(conditions, fmt.Sprintf("%s BETWEEN $%d AND $%d", quotedCol, paramIdx, paramIdx+1))
			args = append(args, vals[0], vals[1])
			paramIdx += 2
		case mink.FilterOpContains:
			cond, arg, err := r.buildContainsCondition(quotedCol, colName, f.Value, paramIdx)
			if err != nil {
				return "", nil, err
			}
			conditions = append(conditions, cond)
			args = append(args, arg)
			paramIdx++
		default:
			return "", nil, fmt.Errorf("mink/postgres/readmodel: unsupported filter operator %q on field %q", f.Op, f.Field)
		}
	}

	if len(conditions) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(conditions, " AND "), args, nil
}

// buildContainsCondition builds a CONTAINS predicate that adapts to how the
// column stores its data:
//   - JSONB columns use the containment operator (@>), matching an array or
//     object that contains the supplied value. The value is JSON-encoded, so a
//     scalar matches array membership and a slice/map matches sub-containment.
//   - All other columns use a case-sensitive substring match. The value is
//     treated literally: LIKE metacharacters (% and _) are escaped so they are
//     not interpreted as wildcards.
func (r *PostgresRepository[T]) buildContainsCondition(quotedCol, colName string, value interface{}, paramIdx int) (string, interface{}, error) {
	if strings.EqualFold(r.columnSQLType(colName), "JSONB") {
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", nil, fmt.Errorf("mink/postgres/readmodel: cannot encode CONTAINS value for column %q: %w", colName, err)
		}
		return fmt.Sprintf("%s @> $%d::jsonb", quotedCol, paramIdx), string(encoded), nil
	}
	return fmt.Sprintf("%s LIKE '%%' || $%d || '%%'", quotedCol, paramIdx), escapeLikePattern(fmt.Sprintf("%v", value)), nil
}

// columnSQLType returns the configured SQL type for a resolved column name,
// or an empty string if the column is unknown.
func (r *PostgresRepository[T]) columnSQLType(colName string) string {
	if r.tableSchema == nil {
		return ""
	}
	for _, col := range r.tableSchema.Columns {
		if col.Name == colName {
			return col.SQLType
		}
	}
	return ""
}

// escapeLikePattern escapes the LIKE metacharacters in s so that the value is
// matched literally inside a substring (CONTAINS) predicate. PostgreSQL's LIKE
// uses backslash as the default escape character.
func escapeLikePattern(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// toInterfaceSlice converts various slice types to []interface{}.
// Supports []interface{}, []string, []int, []int64, []float64, and other slice types via reflection.
func toInterfaceSlice(v interface{}) []interface{} {
	if vals, ok := v.([]interface{}); ok {
		return vals
	}
	if strVals, ok := v.([]string); ok {
		result := make([]interface{}, len(strVals))
		for i, s := range strVals {
			result[i] = s
		}
		return result
	}
	if intVals, ok := v.([]int); ok {
		result := make([]interface{}, len(intVals))
		for i, n := range intVals {
			result[i] = n
		}
		return result
	}
	if int64Vals, ok := v.([]int64); ok {
		result := make([]interface{}, len(int64Vals))
		for i, n := range int64Vals {
			result[i] = n
		}
		return result
	}
	if float64Vals, ok := v.([]float64); ok {
		result := make([]interface{}, len(float64Vals))
		for i, f := range float64Vals {
			result[i] = f
		}
		return result
	}
	// Fallback: use reflection for other slice types
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice {
		result := make([]interface{}, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			result[i] = rv.Index(i).Interface()
		}
		return result
	}
	return nil
}

// buildOrderClause builds the ORDER BY clause.
func (r *PostgresRepository[T]) buildOrderClause(orderBy []mink.OrderBy) string {
	if len(orderBy) == 0 {
		return ""
	}

	var clauses []string
	for _, o := range orderBy {
		colName := r.fieldToColumn(o.Field)
		if colName == "" {
			continue
		}
		dir := "ASC"
		if o.Desc {
			dir = "DESC"
		}
		clauses = append(clauses, fmt.Sprintf("%s %s", quoteIdentifier(colName), dir))
	}

	if len(clauses) == 0 {
		return ""
	}

	return " ORDER BY " + strings.Join(clauses, ", ")
}

// buildLimitClause builds the LIMIT/OFFSET clause.
// Negative values are treated as zero (ignored).
func (r *PostgresRepository[T]) buildLimitClause(limit, offset int) string {
	var clause string
	if limit > 0 {
		clause = fmt.Sprintf(" LIMIT %d", limit)
	}
	if offset > 0 {
		clause += fmt.Sprintf(" OFFSET %d", offset)
	}
	return clause
}

// buildLimitClauseWithValidation builds the LIMIT/OFFSET clause with validation.
// Returns an error if limit or offset are negative.
func (r *PostgresRepository[T]) buildLimitClauseWithValidation(limit, offset int) (string, error) {
	if limit < 0 {
		return "", fmt.Errorf("mink/postgres/readmodel: limit must be non-negative, got %d", limit)
	}
	if offset < 0 {
		return "", fmt.Errorf("mink/postgres/readmodel: offset must be non-negative, got %d", offset)
	}
	return r.buildLimitClause(limit, offset), nil
}

// fieldToColumn maps a struct field name to a database column name.
func (r *PostgresRepository[T]) fieldToColumn(fieldName string) string {
	// First check if it's already a column name
	for _, col := range r.columns {
		if col == fieldName {
			return col
		}
	}

	// Try snake_case conversion
	snakeName := toSnakeCase(fieldName)
	for _, col := range r.columns {
		if col == snakeName {
			return col
		}
	}

	return ""
}

// scanRows scans multiple rows into models.
func (r *PostgresRepository[T]) scanRows(rows *sql.Rows) ([]*T, error) {
	var results []*T

	for rows.Next() {
		model := new(T)
		sc := r.getScanTargets(model)

		if err := rows.Scan(sc.ptrs...); err != nil {
			return nil, r.mapScanError(err)
		}
		if err := sc.run(); err != nil {
			return nil, err
		}

		results = append(results, model)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

// fieldMapper is used to iterate over struct fields and map them to columns.
type fieldMapper struct {
	colToIdx map[string]int
}

// newFieldMapper creates a mapper for the given columns.
func newFieldMapper(columns []string) *fieldMapper {
	colToIdx := make(map[string]int, len(columns))
	for i, col := range columns {
		colToIdx[col] = i
	}
	return &fieldMapper{colToIdx: colToIdx}
}

// getColumnName returns the column name for a struct field.
func getColumnName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("mink")
	if tag == "-" {
		return "", true // skip
	}
	colName := toSnakeCase(field.Name)
	if tag != "" {
		if parts := strings.Split(tag, ","); parts[0] != "" {
			colName = parts[0]
		}
	}
	return colName, false
}

// buildScanPlan precomputes the per-column read plan once at construction: for
// each column, columnField records the struct field index feeding it (-1 if no
// exported field maps to it), and scanKinds records how a NULL is coalesced
// (non-scanKindNone only for a `nullable` column whose field is a non-pointer
// scalar). It resolves fields to columns with the same getColumnName +
// fieldMapper logic getScanTargets used to run per row, so the plan is
// drift-free and the scan path needs no reflection beyond addressing the row.
func (r *PostgresRepository[T]) buildScanPlan() {
	r.scanKinds = make([]scalarScanKind, len(r.columns))
	r.columnField = make([]int, len(r.columns))
	for i := range r.columnField {
		r.columnField[i] = -1
	}

	typ := r.modelType
	mapper := newFieldMapper(r.columns)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		colName, skip := getColumnName(field)
		if skip {
			continue
		}
		if idx, ok := mapper.colToIdx[colName]; ok {
			r.columnField[idx] = i
			if r.tableSchema.Columns[idx].Nullable {
				r.scanKinds[idx] = scalarKindForType(field.Type)
			}
		}
	}
}

// rowScan holds the scan destinations for one row plus any finalizers that copy
// a NULL-coalescing sql.Null[T] holder back into its struct field after Scan. A
// finalizer returns an error when a non-NULL value does not fit the destination
// field (see mink.ColumnValueRangeError).
type rowScan struct {
	ptrs     []interface{}
	finalize []func() error
}

// run applies every finalizer in order, coalescing each wrapped nullable scalar
// column (NULL -> the field's zero value; otherwise the scanned value) and
// returning the first range error. Call it only after a successful
// rows.Scan / row.Scan.
func (rs rowScan) run() error {
	for _, fn := range rs.finalize {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}

// getScanTargets builds the scan destinations for one row from the precomputed
// plan. A nullable scalar column contributes a kind-matched sql.Null[T] holder
// plus a finalizer that copies it back into the field: sql.Null[T]'s zero V
// already IS the Go zero value, so a NULL coalesces without inspecting Valid,
// and an integer/float finalizer additionally range-checks a non-NULL value so
// it fails loudly (mink.ColumnValueRangeError) rather than silently truncating
// or wrapping — parity with a direct database/sql scan, which the wide int64 /
// float64 intermediate would otherwise lose. Every other column scans straight
// into the field address exactly as before, so a model with no nullable scalar
// columns allocates no finalizers and takes the old path.
func (r *PostgresRepository[T]) getScanTargets(model *T) rowScan {
	val := reflect.ValueOf(model).Elem()
	ptrs := make([]interface{}, len(r.columns))
	var finalize []func() error

	for idx := range r.columns {
		fieldIdx := r.columnField[idx]
		if fieldIdx < 0 {
			// No struct field maps to this column: discard destination.
			var discard interface{}
			ptrs[idx] = &discard
			continue
		}

		f := val.Field(fieldIdx)
		switch r.scanKinds[idx] {
		case scanKindString:
			h := new(sql.Null[string])
			ptrs[idx] = h
			finalize = append(finalize, func() error { f.SetString(h.V); return nil })
		case scanKindBool:
			h := new(sql.Null[bool])
			ptrs[idx] = h
			finalize = append(finalize, func() error { f.SetBool(h.V); return nil })
		case scanKindInt:
			h := new(sql.Null[int64])
			ptrs[idx] = h
			finalize = append(finalize, func() error {
				if f.OverflowInt(h.V) {
					return r.rangeError(idx, fmt.Sprintf("%d", h.V))
				}
				f.SetInt(h.V)
				return nil
			})
		case scanKindUint:
			h := new(sql.Null[int64]) // driver surfaces integers as int64
			ptrs[idx] = h
			finalize = append(finalize, func() error {
				// Reject a negative value (a signed column read into an unsigned
				// field) as well as an overflow, matching a direct scan.
				if h.V < 0 || f.OverflowUint(uint64(h.V)) {
					return r.rangeError(idx, fmt.Sprintf("%d", h.V))
				}
				f.SetUint(uint64(h.V))
				return nil
			})
		case scanKindFloat:
			h := new(sql.Null[float64])
			ptrs[idx] = h
			finalize = append(finalize, func() error {
				if f.OverflowFloat(h.V) {
					return r.rangeError(idx, fmt.Sprintf("%g", h.V))
				}
				f.SetFloat(h.V)
				return nil
			})
		case scanKindTime:
			h := new(sql.Null[time.Time])
			ptrs[idx] = h
			finalize = append(finalize, func() error { f.Set(reflect.ValueOf(h.V)); return nil })
		default:
			ptrs[idx] = f.Addr().Interface()
		}
	}

	return rowScan{ptrs: ptrs, finalize: finalize}
}

// rangeError builds a *mink.ColumnValueRangeError for a non-NULL value that does
// not fit column colIdx's destination field. Cold path (out-of-range only), so
// it re-derives the field name/type by reflection.
func (r *PostgresRepository[T]) rangeError(colIdx int, value string) error {
	col := r.columns[colIdx]
	field, goType := r.fieldForColumn(col)
	return &mink.ColumnValueRangeError{Column: col, Field: field, GoType: goType, Value: value}
}

// extractValues extracts field values from a model.
func (r *PostgresRepository[T]) extractValues(model *T) []interface{} {
	val := reflect.ValueOf(model).Elem()
	typ := val.Type()
	mapper := newFieldMapper(r.columns)
	values := make([]interface{}, len(r.columns))

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		colName, skip := getColumnName(field)
		if skip {
			continue
		}
		if idx, ok := mapper.colToIdx[colName]; ok {
			values[idx] = val.Field(i).Interface()
		}
	}
	return values
}

// mapScanError turns database/sql's opaque "converting NULL to <type> is
// unsupported" failure — raised when a NULL lands in a non-nullable scalar
// column scanned directly — into a typed *mink.NullColumnError that names the
// offending column and struct field and suggests the `nullable` tag. The
// nullable-scalar case is already handled by getScanTargets, so this only
// improves the diagnostic for the remaining misconfiguration; it never
// substitutes a value. Any other error (and the case where the column cannot be
// identified) is returned unchanged, and the driver error is preserved via
// Unwrap.
func (r *PostgresRepository[T]) mapScanError(err error) error {
	if err == nil || !strings.Contains(err.Error(), "converting NULL to") {
		return err
	}
	col := parseScanErrorColumn(err.Error())
	if col == "" {
		return err
	}
	field, goType := r.fieldForColumn(col)
	return &mink.NullColumnError{Column: col, Field: field, GoType: goType, Err: err}
}

// parseScanErrorColumn extracts the column name from database/sql's
// `Scan error on column index N, name "col": ...` message. Column names are
// validated identifiers, so the first `name "…"` fragment is unambiguous.
// Returns "" when the fragment is absent (e.g. a future message format).
func parseScanErrorColumn(msg string) string {
	const marker = `name "`
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// fieldForColumn resolves a column name back to its struct field name and Go
// type for error messages. Cold path (scan-error only), so it re-derives the
// mapping by reflection rather than caching. Empty strings if unresolved.
func (r *PostgresRepository[T]) fieldForColumn(colName string) (field, goType string) {
	typ := r.modelType
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		name, skip := getColumnName(f)
		if skip {
			continue
		}
		if name == colName {
			return f.Name, f.Type.String()
		}
	}
	return "", ""
}

// Helper functions

// toSnakeCase converts CamelCase to snake_case.
// It handles acronyms like ID, HTTP, URL correctly:
//   - "OrderID" -> "order_id"
//   - "HTTPServer" -> "http_server"
//   - "CustomerID" -> "customer_id"
func toSnakeCase(s string) string {
	if s == "" {
		return ""
	}

	var result strings.Builder
	runes := []rune(s)

	isUpper := func(r rune) bool { return r >= 'A' && r <= 'Z' }
	isLower := func(r rune) bool { return r >= 'a' && r <= 'z' }
	isDigit := func(r rune) bool { return r >= '0' && r <= '9' }

	for i, r := range runes {
		if i > 0 && isUpper(r) {
			prev := runes[i-1]
			var next rune
			if i+1 < len(runes) {
				next = runes[i+1]
			}

			// Insert underscore in two cases:
			// 1) Transition from lower/digit to upper: "orderID" -> "order_id"
			// 2) End of acronym before lower: "HTTPServer" -> "http_server"
			if isLower(prev) || isDigit(prev) ||
				(isUpper(prev) && next != 0 && isLower(next)) {
				result.WriteByte('_')
			}
		}

		result.WriteRune(r)
	}
	return strings.ToLower(result.String())
}

// safeIndexName creates a PostgreSQL-safe index name that respects the 63-character limit.
// If the combined schema_indexName exceeds 63 chars, it truncates and adds a hash suffix.
func safeIndexName(schema, indexName string) string {
	const maxLen = 63
	const hashLen = 8 // short hash suffix for uniqueness

	fullName := schema + "_" + indexName
	if len(fullName) <= maxLen {
		return fullName
	}

	// Generate a short hash of the full name for uniqueness
	hash := sha256.Sum256([]byte(fullName))
	hashSuffix := hex.EncodeToString(hash[:])[:hashLen]

	// Truncate and append hash: leave room for "_" + hash
	truncateLen := maxLen - hashLen - 1
	truncated := fullName[:truncateLen]

	// Avoid ending with underscore or partial word if possible
	if lastUnderscore := strings.LastIndex(truncated, "_"); lastUnderscore > truncateLen-15 {
		truncated = truncated[:lastUnderscore]
	}

	return truncated + "_" + hashSuffix
}

// goTypeToSQL maps Go types to PostgreSQL types.
func goTypeToSQL(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "TEXT"
	case reflect.Int, reflect.Int32:
		return "INTEGER"
	case reflect.Int64:
		return "BIGINT"
	case reflect.Int8, reflect.Int16:
		return "SMALLINT"
	case reflect.Uint, reflect.Uint32:
		return "INTEGER"
	case reflect.Uint64:
		return "BIGINT"
	case reflect.Uint8, reflect.Uint16:
		return "SMALLINT"
	case reflect.Float32:
		return "REAL"
	case reflect.Float64:
		return "DOUBLE PRECISION"
	case reflect.Bool:
		return "BOOLEAN"
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return "BYTEA"
		}
		return "JSONB"
	case reflect.Map, reflect.Struct:
		// Check for time.Time
		if t.String() == "time.Time" {
			return "TIMESTAMPTZ"
		}
		return "JSONB"
	case reflect.Pointer:
		return goTypeToSQL(t.Elem())
	default:
		return "TEXT"
	}
}

// validateSQLLiteral validates that a value is safe to use in DDL.
// It checks for common SQL injection patterns.
// This is used for default= and type= tag values which are interpolated into DDL.
func validateSQLLiteral(value, context string) error {
	// Check for common SQL injection patterns
	dangerousPatterns := []string{";", "--", "/*", "*/", "\\"}

	for _, pattern := range dangerousPatterns {
		if strings.Contains(value, pattern) {
			return fmt.Errorf("mink/postgres/readmodel: invalid %s value %q: contains forbidden pattern %q", context, value, pattern)
		}
	}

	// Check for SQL keywords using word boundary matching to avoid false positives
	// (e.g., "dropbox" should not be rejected)
	lowerValue := strings.ToLower(value)
	// Note: "with" is intentionally NOT listed — it appears in legitimate types
	// such as "timestamp with time zone".
	dangerousKeywords := []string{
		"drop", "alter", "create", "insert", "update", "delete", "truncate",
		"exec", "execute", "select", "union", "grant", "revoke",
	}
	for _, keyword := range dangerousKeywords {
		if containsWord(lowerValue, keyword) {
			return fmt.Errorf("mink/postgres/readmodel: invalid %s value %q: contains forbidden keyword %q", context, value, keyword)
		}
	}

	return nil
}

// containsWord checks if a string contains a word as a whole word (not as a substring).
// A word boundary is defined as the start/end of string or a non-alphanumeric character.
func containsWord(s, word string) bool {
	idx := 0
	for {
		i := strings.Index(s[idx:], word)
		if i == -1 {
			return false
		}
		pos := idx + i
		endPos := pos + len(word)

		// Check word boundaries
		validStart := pos == 0 || !isAlphanumeric(s[pos-1])
		validEnd := endPos >= len(s) || !isAlphanumeric(s[endPos])

		if validStart && validEnd {
			return true
		}
		idx = pos + 1
		if idx >= len(s) {
			return false
		}
	}
}

// isAlphanumeric returns true if the byte is a letter or digit.
func isAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// defaultForType returns a default value for a SQL type.
func defaultForType(sqlType string) string {
	switch strings.ToUpper(sqlType) {
	case "TEXT", "VARCHAR":
		return "''"
	case "INTEGER", "BIGINT", "SMALLINT", "REAL", "DOUBLE PRECISION":
		return "0"
	case "BOOLEAN":
		return "false"
	case "TIMESTAMPTZ", "TIMESTAMP":
		return "NOW()"
	case "JSONB", "JSON":
		return "'{}'"
	case "BYTEA":
		return "'\\x'::bytea"
	default:
		return "''"
	}
}

// Internal helper methods for database operations.
// These methods accept a dbExecutor interface to support both *sql.DB and *sql.Tx.

// getWithExecutor retrieves a read model by ID using the provided executor.
func (r *PostgresRepository[T]) getWithExecutor(ctx context.Context, exec dbExecutor, id string) (*T, error) {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)
	pkCol := r.columns[r.idIndex]

	selectCols := make([]string, len(r.columns))
	for i, col := range r.columns {
		selectCols[i] = quoteIdentifier(col)
	}

	query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s = $1`,
		strings.Join(selectCols, ", "),
		tableQ,
		quoteIdentifier(pkCol),
	)

	row := exec.QueryRowContext(ctx, query, id)
	model := new(T)
	sc := r.getScanTargets(model)

	if err := row.Scan(sc.ptrs...); err == sql.ErrNoRows {
		return nil, mink.ErrNotFound
	} else if err != nil {
		// A NULL-in-non-nullable failure surfaces as the bare typed error,
		// exactly as the Find/scanRows path returns it; any other scan failure
		// keeps the operation context.
		if mapped := r.mapScanError(err); mapped != err {
			return nil, mapped
		}
		return nil, fmt.Errorf("mink/postgres/readmodel: get failed: %w", err)
	}
	if err := sc.run(); err != nil {
		return nil, err
	}

	return model, nil
}

// insertWithExecutor creates a new read model using the provided executor.
func (r *PostgresRepository[T]) insertWithExecutor(ctx context.Context, exec dbExecutor, model *T) error {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)

	cols := make([]string, len(r.columns))
	placeholders := make([]string, len(r.columns))
	values := r.extractValues(model)

	for i, col := range r.columns {
		cols[i] = quoteIdentifier(col)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	query := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		tableQ,
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
	)

	_, err := exec.ExecContext(ctx, query, values...)
	if err != nil {
		// Detect a PRIMARY KEY / UNIQUE violation via SQLState 23505
		// (isUniqueViolation also falls back to a string match for non-pgconn
		// errors) and surface it as the domain-level ErrAlreadyExists.
		if isUniqueViolation(err) {
			return mink.ErrAlreadyExists
		}
		return fmt.Errorf("mink/postgres/readmodel: insert failed: %w", err)
	}

	return nil
}

// updateWithExecutor modifies an existing read model using the provided executor.
func (r *PostgresRepository[T]) updateWithExecutor(ctx context.Context, exec dbExecutor, id string, updateFn func(*T)) error {
	// Get current model
	model, err := r.getWithExecutor(ctx, exec, id)
	if err != nil {
		return err
	}

	// Apply update function
	updateFn(model)

	// Build UPDATE statement
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)
	pkCol := r.columns[r.idIndex]

	setClauses := make([]string, 0, len(r.columns)-1)
	values := r.extractValues(model)
	args := make([]interface{}, 0, len(r.columns))

	paramIdx := 1
	for i, col := range r.columns {
		if i == r.idIndex {
			continue // skip PK in SET clause
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", quoteIdentifier(col), paramIdx))
		args = append(args, values[i])
		paramIdx++
	}
	args = append(args, id) // for WHERE clause

	query := fmt.Sprintf(`UPDATE %s SET %s WHERE %s = $%d`,
		tableQ,
		strings.Join(setClauses, ", "),
		quoteIdentifier(pkCol),
		paramIdx,
	)

	result, err := exec.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("mink/postgres/readmodel: update failed: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return mink.ErrNotFound
	}

	return nil
}

// upsertWithExecutor creates or updates a read model using the provided executor.
func (r *PostgresRepository[T]) upsertWithExecutor(ctx context.Context, exec dbExecutor, model *T) error {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)
	pkCol := r.columns[r.idIndex]

	cols := make([]string, len(r.columns))
	placeholders := make([]string, len(r.columns))
	updateClauses := make([]string, 0, len(r.columns)-1)
	values := r.extractValues(model)

	for i, col := range r.columns {
		cols[i] = quoteIdentifier(col)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		if i != r.idIndex {
			updateClauses = append(updateClauses,
				fmt.Sprintf("%s = EXCLUDED.%s", quoteIdentifier(col), quoteIdentifier(col)))
		}
	}

	query := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO UPDATE SET %s`,
		tableQ,
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
		quoteIdentifier(pkCol),
		strings.Join(updateClauses, ", "),
	)

	_, err := exec.ExecContext(ctx, query, values...)
	if err != nil {
		return fmt.Errorf("mink/postgres/readmodel: upsert failed: %w", err)
	}

	return nil
}

// deleteWithExecutor removes a read model by ID using the provided executor.
func (r *PostgresRepository[T]) deleteWithExecutor(ctx context.Context, exec dbExecutor, id string) error {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)
	pkCol := r.columns[r.idIndex]

	query := fmt.Sprintf(`DELETE FROM %s WHERE %s = $1`,
		tableQ,
		quoteIdentifier(pkCol),
	)

	result, err := exec.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("mink/postgres/readmodel: delete failed: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return mink.ErrNotFound
	}

	return nil
}

// getManyWithExecutor retrieves multiple read models by their IDs using the provided executor.
func (r *PostgresRepository[T]) getManyWithExecutor(ctx context.Context, exec dbExecutor, ids []string) ([]*T, error) {
	if len(ids) == 0 {
		return []*T{}, nil
	}

	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)
	pkCol := r.columns[r.idIndex]

	selectCols := make([]string, len(r.columns))
	for i, col := range r.columns {
		selectCols[i] = quoteIdentifier(col)
	}

	// Build parameterized IN clause
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s IN (%s)`,
		strings.Join(selectCols, ", "),
		tableQ,
		quoteIdentifier(pkCol),
		strings.Join(placeholders, ", "),
	)

	rows, err := exec.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/readmodel: getMany failed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return r.scanRows(rows)
}

// findWithExecutor queries read models with the given criteria using the provided executor.
func (r *PostgresRepository[T]) findWithExecutor(ctx context.Context, exec dbExecutor, query mink.Query) ([]*T, error) {
	sqlQuery, args, err := r.buildSelectQuery(query)
	if err != nil {
		return nil, err
	}

	rows, err := exec.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/readmodel: find failed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return r.scanRows(rows)
}

// findOneWithExecutor returns the first read model matching the query using the provided executor.
func (r *PostgresRepository[T]) findOneWithExecutor(ctx context.Context, exec dbExecutor, query mink.Query) (*T, error) {
	query.Limit = 1
	results, err := r.findWithExecutor(ctx, exec, query)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, mink.ErrNotFound
	}
	return results[0], nil
}

// countWithExecutor returns the count of matching read models using the provided executor.
func (r *PostgresRepository[T]) countWithExecutor(ctx context.Context, exec dbExecutor, query mink.Query) (int64, error) {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)

	whereClause, args, err := r.buildWhereClause(query.Filters)
	if err != nil {
		return 0, err
	}

	sqlQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s%s`, tableQ, whereClause)

	var count int64
	err = exec.QueryRowContext(ctx, sqlQuery, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/readmodel: count failed: %w", err)
	}

	return count, nil
}

// deleteManyWithExecutor removes all read models matching the query using the provided executor.
func (r *PostgresRepository[T]) deleteManyWithExecutor(ctx context.Context, exec dbExecutor, query mink.Query) (int64, error) {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)

	whereClause, args, err := r.buildWhereClause(query.Filters)
	if err != nil {
		return 0, err
	}

	sqlQuery := fmt.Sprintf(`DELETE FROM %s%s`, tableQ, whereClause)

	result, err := exec.ExecContext(ctx, sqlQuery, args...)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/readmodel: deleteMany failed: %w", err)
	}

	return result.RowsAffected()
}

// clearWithExecutor removes all read models using the provided executor.
func (r *PostgresRepository[T]) clearWithExecutor(ctx context.Context, exec dbExecutor) error {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)

	_, err := exec.ExecContext(ctx, fmt.Sprintf(`TRUNCATE TABLE %s`, tableQ))
	if err != nil {
		return fmt.Errorf("mink/postgres/readmodel: clear failed: %w", err)
	}

	return nil
}

// existsWithExecutor checks if a read model with the given ID exists using the provided executor.
func (r *PostgresRepository[T]) existsWithExecutor(ctx context.Context, exec dbExecutor, id string) (bool, error) {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)
	pkCol := r.columns[r.idIndex]

	var exists bool
	query := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE %s = $1)`,
		tableQ,
		quoteIdentifier(pkCol),
	)

	err := exec.QueryRowContext(ctx, query, id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("mink/postgres/readmodel: exists check failed: %w", err)
	}

	return exists, nil
}

// Transaction support

// WithTx creates a new repository instance that uses the provided transaction.
func (r *PostgresRepository[T]) WithTx(tx *sql.Tx) *TxRepository[T] {
	return &TxRepository[T]{
		repo: r,
		tx:   tx,
	}
}

// TxRepository wraps PostgresRepository for transaction support.
// It implements all ReadModelRepository methods within a transaction context.
type TxRepository[T any] struct {
	repo *PostgresRepository[T]
	tx   *sql.Tx
}

// Get retrieves a read model by ID within a transaction.
func (tr *TxRepository[T]) Get(ctx context.Context, id string) (*T, error) {
	return tr.repo.getWithExecutor(ctx, tr.tx, id)
}

// GetMany retrieves multiple read models by their IDs within a transaction.
func (tr *TxRepository[T]) GetMany(ctx context.Context, ids []string) ([]*T, error) {
	return tr.repo.getManyWithExecutor(ctx, tr.tx, ids)
}

// Find queries read models with the given criteria within a transaction.
func (tr *TxRepository[T]) Find(ctx context.Context, query mink.Query) ([]*T, error) {
	return tr.repo.findWithExecutor(ctx, tr.tx, query)
}

// FindOne returns the first read model matching the query within a transaction.
func (tr *TxRepository[T]) FindOne(ctx context.Context, query mink.Query) (*T, error) {
	return tr.repo.findOneWithExecutor(ctx, tr.tx, query)
}

// Count returns the number of read models matching the query within a transaction.
func (tr *TxRepository[T]) Count(ctx context.Context, query mink.Query) (int64, error) {
	return tr.repo.countWithExecutor(ctx, tr.tx, query)
}

// Insert creates a new read model within a transaction.
func (tr *TxRepository[T]) Insert(ctx context.Context, model *T) error {
	return tr.repo.insertWithExecutor(ctx, tr.tx, model)
}

// Update modifies an existing read model within a transaction.
func (tr *TxRepository[T]) Update(ctx context.Context, id string, updateFn func(*T)) error {
	return tr.repo.updateWithExecutor(ctx, tr.tx, id, updateFn)
}

// Upsert creates or updates a read model within a transaction.
func (tr *TxRepository[T]) Upsert(ctx context.Context, model *T) error {
	return tr.repo.upsertWithExecutor(ctx, tr.tx, model)
}

// Delete removes a read model within a transaction.
func (tr *TxRepository[T]) Delete(ctx context.Context, id string) error {
	return tr.repo.deleteWithExecutor(ctx, tr.tx, id)
}

// DeleteMany removes all read models matching the query within a transaction.
func (tr *TxRepository[T]) DeleteMany(ctx context.Context, query mink.Query) (int64, error) {
	return tr.repo.deleteManyWithExecutor(ctx, tr.tx, query)
}

// Clear removes all read models within a transaction.
func (tr *TxRepository[T]) Clear(ctx context.Context) error {
	return tr.repo.clearWithExecutor(ctx, tr.tx)
}

// Exists checks if a read model with the given ID exists within a transaction.
func (tr *TxRepository[T]) Exists(ctx context.Context, id string) (bool, error) {
	return tr.repo.existsWithExecutor(ctx, tr.tx, id)
}

// GetAll returns all read models in the repository within a transaction.
func (tr *TxRepository[T]) GetAll(ctx context.Context) ([]*T, error) {
	return tr.repo.findWithExecutor(ctx, tr.tx, mink.Query{})
}

// Ensure interface compliance
var _ mink.ReadModelRepository[any] = (*PostgresRepository[any])(nil)
