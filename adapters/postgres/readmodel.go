// Package postgres provides a PostgreSQL implementation of the read model repository.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	mink "github.com/AshkanYarmoradi/go-mink"
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
}

// NewPostgresRepository creates a new PostgreSQL-backed repository for read models.
// The type T should be a struct with db tags for column mapping.
//
// Supported struct tags:
//   - `mink:"column_name"` - Column name (default: snake_case of field name)
//   - `mink:"-"` - Skip this field
//   - `mink:"column_name,pk"` - Primary key
//   - `mink:"column_name,index"` - Create index
//   - `mink:"column_name,unique"` - Unique constraint
//   - `mink:"column_name,nullable"` - Allow NULL values
//   - `mink:"column_name,default=value"` - Default value
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
func NewPostgresRepository[T any](db *sql.DB, opts ...ReadModelOption) (*PostgresRepository[T], error) {
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
		if typ.Kind() == reflect.Ptr {
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

	// Auto-migrate if enabled
	if config.autoMigrate {
		if err := repo.Migrate(context.Background()); err != nil {
			return nil, fmt.Errorf("mink/postgres/readmodel: migration failed: %w", err)
		}
	}

	return repo, nil
}

// buildTableSchema analyzes the struct type and builds a table schema.
func (r *PostgresRepository[T]) buildTableSchema() (*TableSchema, error) {
	var t T
	typ := reflect.TypeOf(t)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("type must be a struct, got %s", typ.Kind())
	}

	schema := &TableSchema{
		TableName: r.config.tableName,
		Schema:    r.config.schema,
	}

	hasPK := false
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

		// Check if this is the ID field (fallback if no pk tag)
		if field.Name == r.config.idField && !hasPK {
			col.PrimaryKey = true
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

	// If still no PK, use first column
	if !hasPK && len(schema.Columns) > 0 {
		schema.Columns[0].PrimaryKey = true
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

	// Create indexes
	for _, idx := range r.tableSchema.Indexes {
		if err := validateIdentifier(idx.Name, "index"); err != nil {
			return err
		}

		// Build full index name and validate length
		// PostgreSQL has a 63 character limit for identifiers
		fullIdxName := r.config.schema + "_" + idx.Name
		if len(fullIdxName) > 63 {
			return fmt.Errorf("mink/postgres/readmodel: index name %q exceeds PostgreSQL's 63 character limit (%d chars)", fullIdxName, len(fullIdxName))
		}

		uniqueStr := ""
		if idx.Unique {
			uniqueStr = "UNIQUE "
		}

		quotedCols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			quotedCols[i] = quoteIdentifier(c)
		}

		// Use schema-qualified index name
		idxName := quoteIdentifier(r.config.schema + "_" + idx.Name)
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

	// Handle schema evolution - add missing columns
	err = r.migrateColumns(ctx, pkColumn)
	if err != nil {
		return fmt.Errorf("failed to migrate columns: %w", err)
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
	defer rows.Close()

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

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/readmodel: getMany failed: %w", err)
	}
	defer rows.Close()

	return r.scanRows(rows)
}

// Find queries read models with the given criteria.
func (r *PostgresRepository[T]) Find(ctx context.Context, query mink.Query) ([]*T, error) {
	sqlQuery, args := r.buildSelectQuery(query)

	rows, err := r.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/readmodel: find failed: %w", err)
	}
	defer rows.Close()

	return r.scanRows(rows)
}

// FindOne returns the first read model matching the query.
func (r *PostgresRepository[T]) FindOne(ctx context.Context, query mink.Query) (*T, error) {
	query.Limit = 1
	results, err := r.Find(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, mink.ErrNotFound
	}
	return results[0], nil
}

// Count returns the number of read models matching the query.
func (r *PostgresRepository[T]) Count(ctx context.Context, query mink.Query) (int64, error) {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)

	whereClause, args := r.buildWhereClause(query.Filters)

	sqlQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s%s`, tableQ, whereClause)

	var count int64
	err := r.db.QueryRowContext(ctx, sqlQuery, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/readmodel: count failed: %w", err)
	}

	return count, nil
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
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)

	whereClause, args := r.buildWhereClause(query.Filters)

	sqlQuery := fmt.Sprintf(`DELETE FROM %s%s`, tableQ, whereClause)

	result, err := r.db.ExecContext(ctx, sqlQuery, args...)
	if err != nil {
		return 0, fmt.Errorf("mink/postgres/readmodel: deleteMany failed: %w", err)
	}

	return result.RowsAffected()
}

// Clear removes all read models.
func (r *PostgresRepository[T]) Clear(ctx context.Context) error {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)

	_, err := r.db.ExecContext(ctx, fmt.Sprintf(`TRUNCATE TABLE %s`, tableQ))
	if err != nil {
		return fmt.Errorf("mink/postgres/readmodel: clear failed: %w", err)
	}

	return nil
}

// Exists checks if a read model with the given ID exists.
func (r *PostgresRepository[T]) Exists(ctx context.Context, id string) (bool, error) {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)
	pkCol := r.columns[r.idIndex]

	var exists bool
	query := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE %s = $1)`,
		tableQ,
		quoteIdentifier(pkCol),
	)

	err := r.db.QueryRowContext(ctx, query, id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("mink/postgres/readmodel: exists check failed: %w", err)
	}

	return exists, nil
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
func (r *PostgresRepository[T]) buildSelectQuery(query mink.Query) (string, []interface{}) {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)

	selectCols := make([]string, len(r.columns))
	for i, col := range r.columns {
		selectCols[i] = quoteIdentifier(col)
	}

	whereClause, args := r.buildWhereClause(query.Filters)
	orderClause := r.buildOrderClause(query.OrderBy)
	limitClause := r.buildLimitClause(query.Limit, query.Offset)

	sqlQuery := fmt.Sprintf(`SELECT %s FROM %s%s%s%s`,
		strings.Join(selectCols, ", "),
		tableQ,
		whereClause,
		orderClause,
		limitClause,
	)

	return sqlQuery, args
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
func (r *PostgresRepository[T]) buildWhereClause(filters []mink.Filter) (string, []interface{}) {
	if len(filters) == 0 {
		return "", nil
	}

	var conditions []string
	var args []interface{}
	paramIdx := 1

	for _, f := range filters {
		colName := r.fieldToColumn(f.Field)
		if colName == "" {
			continue
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
			if vals := toInterfaceSlice(f.Value); vals != nil {
				conditions = append(conditions, buildInClause(quotedCol, "IN", vals, &paramIdx, &args))
			}
		case mink.FilterOpNotIn:
			if vals := toInterfaceSlice(f.Value); vals != nil {
				conditions = append(conditions, buildInClause(quotedCol, "NOT IN", vals, &paramIdx, &args))
			}
		case mink.FilterOpIsNull:
			conditions = append(conditions, fmt.Sprintf("%s IS NULL", quotedCol))
		case mink.FilterOpIsNotNull:
			conditions = append(conditions, fmt.Sprintf("%s IS NOT NULL", quotedCol))
		case mink.FilterOpBetween:
			if vals, ok := f.Value.([]interface{}); ok && len(vals) == 2 {
				conditions = append(conditions, fmt.Sprintf("%s BETWEEN $%d AND $%d", quotedCol, paramIdx, paramIdx+1))
				args = append(args, vals[0], vals[1])
				paramIdx += 2
			}
		}
	}

	if len(conditions) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

// toInterfaceSlice converts various slice types to []interface{}.
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
		ptrs := r.getScanPointers(model)

		if err := rows.Scan(ptrs...); err != nil {
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

// getScanPointers returns pointers to struct fields for scanning.
func (r *PostgresRepository[T]) getScanPointers(model *T) []interface{} {
	val := reflect.ValueOf(model).Elem()
	typ := val.Type()
	mapper := newFieldMapper(r.columns)
	ptrs := make([]interface{}, len(r.columns))

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
			ptrs[idx] = val.Field(i).Addr().Interface()
		}
	}

	// Fill any nil pointers with discard destinations
	for i, ptr := range ptrs {
		if ptr == nil {
			var discard interface{}
			ptrs[i] = &discard
		}
	}
	return ptrs
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
	case reflect.Ptr:
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
	lowerValue := strings.ToLower(value)

	for _, pattern := range dangerousPatterns {
		if strings.Contains(value, pattern) {
			return fmt.Errorf("mink/postgres/readmodel: invalid %s value %q: contains forbidden pattern %q", context, value, pattern)
		}
	}

	// Check for SQL keywords that shouldn't appear in literals
	dangerousKeywords := []string{"drop ", "alter ", "create ", "insert ", "update ", "delete ", "truncate ", "exec ", "execute "}
	for _, keyword := range dangerousKeywords {
		if strings.Contains(lowerValue, keyword) {
			return fmt.Errorf("mink/postgres/readmodel: invalid %s value %q: contains forbidden keyword", context, value)
		}
	}

	return nil
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
		return "''"
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
	ptrs := r.getScanPointers(model)

	if err := row.Scan(ptrs...); err == sql.ErrNoRows {
		return nil, mink.ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("mink/postgres/readmodel: get failed: %w", err)
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
		if strings.Contains(err.Error(), "duplicate key") ||
			strings.Contains(err.Error(), "unique constraint") {
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

// Transaction support

// WithTx creates a new repository instance that uses the provided transaction.
func (r *PostgresRepository[T]) WithTx(tx *sql.Tx) *TxRepository[T] {
	return &TxRepository[T]{
		repo: r,
		tx:   tx,
	}
}

// TxRepository wraps PostgresRepository for transaction support.
type TxRepository[T any] struct {
	repo *PostgresRepository[T]
	tx   *sql.Tx
}

// Get retrieves a read model by ID within a transaction.
func (tr *TxRepository[T]) Get(ctx context.Context, id string) (*T, error) {
	return tr.repo.getWithExecutor(ctx, tr.tx, id)
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

// Ensure interface compliance
var _ mink.ReadModelRepository[any] = (*PostgresRepository[any])(nil)
