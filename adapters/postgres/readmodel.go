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
				col.Default = strings.TrimPrefix(part, "default=")
			case strings.HasPrefix(part, "type="):
				col.SQLType = strings.TrimPrefix(part, "type=")
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

		colDef := fmt.Sprintf("%s %s", quoteIdentifier(col.Name), col.SQLType)
		if col.Default != "" {
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

	row := r.db.QueryRowContext(ctx, query, id)

	model, err := r.scanRow(row)
	if err == sql.ErrNoRows {
		return nil, mink.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mink/postgres/readmodel: get failed: %w", err)
	}

	return model, nil
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

	_, err := r.db.ExecContext(ctx, query, values...)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") ||
			strings.Contains(err.Error(), "unique constraint") {
			return mink.ErrAlreadyExists
		}
		return fmt.Errorf("mink/postgres/readmodel: insert failed: %w", err)
	}

	return nil
}

// Update modifies an existing read model.
func (r *PostgresRepository[T]) Update(ctx context.Context, id string, updateFn func(*T)) error {
	// Get current model
	model, err := r.Get(ctx, id)
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

	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("mink/postgres/readmodel: update failed: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return mink.ErrNotFound
	}

	return nil
}

// Upsert creates or updates a read model.
func (r *PostgresRepository[T]) Upsert(ctx context.Context, model *T) error {
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

	_, err := r.db.ExecContext(ctx, query, values...)
	if err != nil {
		return fmt.Errorf("mink/postgres/readmodel: upsert failed: %w", err)
	}

	return nil
}

// Delete removes a read model by ID.
func (r *PostgresRepository[T]) Delete(ctx context.Context, id string) error {
	tableQ := quoteQualifiedTable(r.config.schema, r.config.tableName)
	pkCol := r.columns[r.idIndex]

	query := fmt.Sprintf(`DELETE FROM %s WHERE %s = $1`,
		tableQ,
		quoteIdentifier(pkCol),
	)

	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("mink/postgres/readmodel: delete failed: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return mink.ErrNotFound
	}

	return nil
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

		switch f.Op {
		case mink.FilterOpEq:
			conditions = append(conditions, fmt.Sprintf("%s = $%d", quotedCol, paramIdx))
			args = append(args, f.Value)
			paramIdx++
		case mink.FilterOpNe:
			conditions = append(conditions, fmt.Sprintf("%s != $%d", quotedCol, paramIdx))
			args = append(args, f.Value)
			paramIdx++
		case mink.FilterOpGt:
			conditions = append(conditions, fmt.Sprintf("%s > $%d", quotedCol, paramIdx))
			args = append(args, f.Value)
			paramIdx++
		case mink.FilterOpGte:
			conditions = append(conditions, fmt.Sprintf("%s >= $%d", quotedCol, paramIdx))
			args = append(args, f.Value)
			paramIdx++
		case mink.FilterOpLt:
			conditions = append(conditions, fmt.Sprintf("%s < $%d", quotedCol, paramIdx))
			args = append(args, f.Value)
			paramIdx++
		case mink.FilterOpLte:
			conditions = append(conditions, fmt.Sprintf("%s <= $%d", quotedCol, paramIdx))
			args = append(args, f.Value)
			paramIdx++
		case mink.FilterOpLike:
			conditions = append(conditions, fmt.Sprintf("%s LIKE $%d", quotedCol, paramIdx))
			args = append(args, f.Value)
			paramIdx++
		case mink.FilterOpIn:
			// Handle IN clause with multiple values
			if vals, ok := f.Value.([]interface{}); ok {
				placeholders := make([]string, len(vals))
				for i, v := range vals {
					placeholders[i] = fmt.Sprintf("$%d", paramIdx)
					args = append(args, v)
					paramIdx++
				}
				conditions = append(conditions, fmt.Sprintf("%s IN (%s)", quotedCol, strings.Join(placeholders, ", ")))
			} else if strVals, ok := f.Value.([]string); ok {
				placeholders := make([]string, len(strVals))
				for i, v := range strVals {
					placeholders[i] = fmt.Sprintf("$%d", paramIdx)
					args = append(args, v)
					paramIdx++
				}
				conditions = append(conditions, fmt.Sprintf("%s IN (%s)", quotedCol, strings.Join(placeholders, ", ")))
			}
		case mink.FilterOpNotIn:
			if vals, ok := f.Value.([]interface{}); ok {
				placeholders := make([]string, len(vals))
				for i, v := range vals {
					placeholders[i] = fmt.Sprintf("$%d", paramIdx)
					args = append(args, v)
					paramIdx++
				}
				conditions = append(conditions, fmt.Sprintf("%s NOT IN (%s)", quotedCol, strings.Join(placeholders, ", ")))
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

// scanRow scans a single row into a model.
func (r *PostgresRepository[T]) scanRow(row *sql.Row) (*T, error) {
	model := new(T)
	ptrs := r.getScanPointers(model)

	if err := row.Scan(ptrs...); err != nil {
		return nil, err
	}

	return model, nil
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

// getScanPointers returns pointers to struct fields for scanning.
func (r *PostgresRepository[T]) getScanPointers(model *T) []interface{} {
	val := reflect.ValueOf(model).Elem()
	typ := val.Type()

	ptrs := make([]interface{}, len(r.columns))
	colToIdx := make(map[string]int)

	for i, col := range r.columns {
		colToIdx[col] = i
	}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}

		colName := toSnakeCase(field.Name)
		tag := field.Tag.Get("mink")
		if tag == "-" {
			continue
		}
		if tag != "" {
			parts := strings.Split(tag, ",")
			if parts[0] != "" {
				colName = parts[0]
			}
		}

		if idx, ok := colToIdx[colName]; ok {
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

	colToIdx := make(map[string]int)
	for i, col := range r.columns {
		colToIdx[col] = i
	}

	values := make([]interface{}, len(r.columns))

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}

		colName := toSnakeCase(field.Name)
		tag := field.Tag.Get("mink")
		if tag == "-" {
			continue
		}
		if tag != "" {
			parts := strings.Split(tag, ",")
			if parts[0] != "" {
				colName = parts[0]
			}
		}

		if idx, ok := colToIdx[colName]; ok {
			values[idx] = val.Field(i).Interface()
		}
	}

	return values
}

// Helper functions

// toSnakeCase converts CamelCase to snake_case.
func toSnakeCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteByte('_')
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
	tableQ := quoteQualifiedTable(tr.repo.config.schema, tr.repo.config.tableName)
	pkCol := tr.repo.columns[tr.repo.idIndex]

	selectCols := make([]string, len(tr.repo.columns))
	for i, col := range tr.repo.columns {
		selectCols[i] = quoteIdentifier(col)
	}

	query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s = $1`,
		strings.Join(selectCols, ", "),
		tableQ,
		quoteIdentifier(pkCol),
	)

	row := tr.tx.QueryRowContext(ctx, query, id)
	model := new(T)
	ptrs := tr.repo.getScanPointers(model)

	if err := row.Scan(ptrs...); err == sql.ErrNoRows {
		return nil, mink.ErrNotFound
	} else if err != nil {
		return nil, err
	}

	return model, nil
}

// Insert creates a new read model within a transaction.
func (tr *TxRepository[T]) Insert(ctx context.Context, model *T) error {
	tableQ := quoteQualifiedTable(tr.repo.config.schema, tr.repo.config.tableName)

	cols := make([]string, len(tr.repo.columns))
	placeholders := make([]string, len(tr.repo.columns))
	values := tr.repo.extractValues(model)

	for i, col := range tr.repo.columns {
		cols[i] = quoteIdentifier(col)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	query := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		tableQ,
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
	)

	_, err := tr.tx.ExecContext(ctx, query, values...)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return mink.ErrAlreadyExists
		}
		return err
	}

	return nil
}

// Update modifies an existing read model within a transaction.
func (tr *TxRepository[T]) Update(ctx context.Context, id string, updateFn func(*T)) error {
	model, err := tr.Get(ctx, id)
	if err != nil {
		return err
	}

	updateFn(model)

	tableQ := quoteQualifiedTable(tr.repo.config.schema, tr.repo.config.tableName)
	pkCol := tr.repo.columns[tr.repo.idIndex]

	setClauses := make([]string, 0, len(tr.repo.columns)-1)
	values := tr.repo.extractValues(model)
	args := make([]interface{}, 0, len(tr.repo.columns))

	paramIdx := 1
	for i, col := range tr.repo.columns {
		if i == tr.repo.idIndex {
			continue
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", quoteIdentifier(col), paramIdx))
		args = append(args, values[i])
		paramIdx++
	}
	args = append(args, id)

	query := fmt.Sprintf(`UPDATE %s SET %s WHERE %s = $%d`,
		tableQ,
		strings.Join(setClauses, ", "),
		quoteIdentifier(pkCol),
		paramIdx,
	)

	_, err = tr.tx.ExecContext(ctx, query, args...)
	return err
}

// Upsert creates or updates a read model within a transaction.
func (tr *TxRepository[T]) Upsert(ctx context.Context, model *T) error {
	tableQ := quoteQualifiedTable(tr.repo.config.schema, tr.repo.config.tableName)
	pkCol := tr.repo.columns[tr.repo.idIndex]

	cols := make([]string, len(tr.repo.columns))
	placeholders := make([]string, len(tr.repo.columns))
	updateClauses := make([]string, 0, len(tr.repo.columns)-1)
	values := tr.repo.extractValues(model)

	for i, col := range tr.repo.columns {
		cols[i] = quoteIdentifier(col)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		if i != tr.repo.idIndex {
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

	_, err := tr.tx.ExecContext(ctx, query, values...)
	return err
}

// Delete removes a read model within a transaction.
func (tr *TxRepository[T]) Delete(ctx context.Context, id string) error {
	tableQ := quoteQualifiedTable(tr.repo.config.schema, tr.repo.config.tableName)
	pkCol := tr.repo.columns[tr.repo.idIndex]

	query := fmt.Sprintf(`DELETE FROM %s WHERE %s = $1`,
		tableQ,
		quoteIdentifier(pkCol),
	)

	result, err := tr.tx.ExecContext(ctx, query, id)
	if err != nil {
		return err
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return mink.ErrNotFound
	}

	return nil
}

// Ensure interface compliance
var _ mink.ReadModelRepository[any] = (*PostgresRepository[any])(nil)
