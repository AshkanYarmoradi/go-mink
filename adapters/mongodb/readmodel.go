package mongodb

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	mink "go-mink.dev"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var (
	_ mink.ReadModelRepository[any] = (*MongoRepository[any])(nil)
	_ mink.ReadModelRepository[any] = (*TxRepository[any])(nil)
)

// MongoRepository provides a MongoDB implementation of ReadModelRepository.
type MongoRepository[T any] struct {
	client *mongo.Client
	db     *mongo.Database
	coll   *mongo.Collection
	config mongoRepositoryConfig
	model  readModelInfo
}

type mongoRepositoryConfig struct {
	database    string
	collection  string
	idField     string
	autoMigrate bool
}

// MongoRepositoryOption configures a MongoRepository.
type MongoRepositoryOption func(*mongoRepositoryConfig)

// WithReadModelCollection sets the collection name for the read model.
func WithReadModelCollection(name string) MongoRepositoryOption {
	return func(c *mongoRepositoryConfig) {
		c.collection = name
	}
}

// WithReadModelIDField sets the struct field used as the primary key.
func WithReadModelIDField(field string) MongoRepositoryOption {
	return func(c *mongoRepositoryConfig) {
		c.idField = field
	}
}

// WithReadModelAutoMigrate controls index creation.
func WithReadModelAutoMigrate(enabled bool) MongoRepositoryOption {
	return func(c *mongoRepositoryConfig) {
		c.autoMigrate = enabled
	}
}

// NewMongoRepository creates a MongoDB-backed repository for read models.
func NewMongoRepository[T any](client *mongo.Client, database string, opts ...MongoRepositoryOption) (*MongoRepository[T], error) {
	if client == nil {
		return nil, fmt.Errorf("mink/mongodb/readmodel: client is nil")
	}
	cfg := mongoRepositoryConfig{database: database, idField: "ID", autoMigrate: true}
	if cfg.database == "" {
		cfg.database = "mink"
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	info, err := buildReadModelInfo[T](cfg.collection, cfg.idField)
	if err != nil {
		return nil, err
	}
	cfg.collection = info.collection

	repo := &MongoRepository[T]{
		client: client,
		db:     client.Database(cfg.database),
		config: cfg,
		model:  info,
	}
	repo.coll = repo.db.Collection(cfg.collection)
	if cfg.autoMigrate {
		if err := repo.Migrate(context.Background()); err != nil {
			return nil, err
		}
	}
	return repo, nil
}

// NewMongoRepositoryFromAdapter creates a read model repository from an adapter.
func NewMongoRepositoryFromAdapter[T any](adapter *MongoAdapter, opts ...MongoRepositoryOption) (*MongoRepository[T], error) {
	all := append([]MongoRepositoryOption{func(c *mongoRepositoryConfig) { c.database = adapter.database }}, opts...)
	return NewMongoRepository[T](adapter.client, adapter.database, all...)
}

// Migrate creates read model indexes.
func (r *MongoRepository[T]) Migrate(ctx context.Context) error {
	var models []mongo.IndexModel
	for _, field := range r.model.fields {
		if field.index || field.unique {
			models = append(models, mongo.IndexModel{
				Keys:    bson.D{{Key: field.name, Value: 1}},
				Options: options.Index().SetName("idx_" + r.config.collection + "_" + field.name).SetUnique(field.unique),
			})
		}
	}
	if len(models) == 0 {
		return nil
	}
	_, err := r.coll.Indexes().CreateMany(ctx, models)
	return err
}

// Get retrieves a read model by ID.
func (r *MongoRepository[T]) Get(ctx context.Context, id string) (*T, error) {
	var doc bson.M
	err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, mink.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.modelToStruct(doc)
}

// GetMany retrieves multiple read models by their IDs.
func (r *MongoRepository[T]) GetMany(ctx context.Context, ids []string) ([]*T, error) {
	if len(ids) == 0 {
		return []*T{}, nil
	}
	cursor, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()
	return r.scanModels(ctx, cursor)
}

// Find queries read models with the given criteria.
func (r *MongoRepository[T]) Find(ctx context.Context, query mink.Query) ([]*T, error) {
	findOpts, err := r.findOptions(query)
	if err != nil {
		return nil, err
	}
	cursor, err := r.coll.Find(ctx, r.buildFilter(query.Filters), findOpts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close(ctx) }()
	return r.scanModels(ctx, cursor)
}

// FindOne returns the first read model matching the query.
func (r *MongoRepository[T]) FindOne(ctx context.Context, query mink.Query) (*T, error) {
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
func (r *MongoRepository[T]) Count(ctx context.Context, query mink.Query) (int64, error) {
	return r.coll.CountDocuments(ctx, r.buildFilter(query.Filters))
}

// Insert creates a new read model.
func (r *MongoRepository[T]) Insert(ctx context.Context, model *T) error {
	doc, err := r.structToDocument(model)
	if err != nil {
		return err
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return mink.ErrAlreadyExists
		}
		return err
	}
	return nil
}

// Update modifies an existing read model.
func (r *MongoRepository[T]) Update(ctx context.Context, id string, updateFn func(*T)) error {
	model, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	updateFn(model)
	doc, err := r.structToDocument(model)
	if err != nil {
		return err
	}
	res, err := r.coll.ReplaceOne(ctx, bson.M{"_id": id}, doc)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return mink.ErrNotFound
	}
	return nil
}

// Upsert creates or updates a read model.
func (r *MongoRepository[T]) Upsert(ctx context.Context, model *T) error {
	doc, err := r.structToDocument(model)
	if err != nil {
		return err
	}
	_, err = r.coll.ReplaceOne(ctx, bson.M{"_id": doc["_id"]}, doc, options.Replace().SetUpsert(true))
	return err
}

// Delete removes a read model by ID.
func (r *MongoRepository[T]) Delete(ctx context.Context, id string) error {
	res, err := r.coll.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return mink.ErrNotFound
	}
	return nil
}

// DeleteMany removes all read models matching the query.
func (r *MongoRepository[T]) DeleteMany(ctx context.Context, query mink.Query) (int64, error) {
	res, err := r.coll.DeleteMany(ctx, r.buildFilter(query.Filters))
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// Clear removes all read models.
func (r *MongoRepository[T]) Clear(ctx context.Context) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{})
	return err
}

// Exists checks if a read model exists.
func (r *MongoRepository[T]) Exists(ctx context.Context, id string) (bool, error) {
	count, err := r.coll.CountDocuments(ctx, bson.M{"_id": id})
	return count > 0, err
}

// GetAll returns all read models.
func (r *MongoRepository[T]) GetAll(ctx context.Context) ([]*T, error) {
	return r.Find(ctx, mink.Query{})
}

// WithSessionContext returns a transaction-scoped repository using the MongoDB session in ctx.
func (r *MongoRepository[T]) WithSessionContext(ctx context.Context) *TxRepository[T] {
	return &TxRepository[T]{repo: r, sessionCtx: ctx}
}

// RunTransaction runs fn inside a MongoDB transaction with a transaction-scoped repository.
func (r *MongoRepository[T]) RunTransaction(ctx context.Context, fn func(context.Context, *TxRepository[T]) error, opts ...options.Lister[options.TransactionOptions]) error {
	if fn == nil {
		return fmt.Errorf("mink/mongodb/readmodel: transaction callback is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := r.client.StartSession()
	if err != nil {
		return fmt.Errorf("mink/mongodb/readmodel: failed to start transaction session: %w", err)
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(sc context.Context) (any, error) {
		return nil, fn(sc, r.WithSessionContext(sc))
	}, opts...)
	return err
}

// TxRepository executes read-model operations with a MongoDB session context.
type TxRepository[T any] struct {
	repo       *MongoRepository[T]
	sessionCtx context.Context
}

func (tr *TxRepository[T]) contextFor(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if tr == nil || tr.sessionCtx == nil {
		return ctx
	}
	session := mongo.SessionFromContext(tr.sessionCtx)
	if session == nil {
		return ctx
	}
	return mongo.NewSessionContext(ctx, session)
}

// Get retrieves a read model by ID within the transaction context.
func (tr *TxRepository[T]) Get(ctx context.Context, id string) (*T, error) {
	return tr.repo.Get(tr.contextFor(ctx), id)
}

// GetMany retrieves multiple read models by ID within the transaction context.
func (tr *TxRepository[T]) GetMany(ctx context.Context, ids []string) ([]*T, error) {
	return tr.repo.GetMany(tr.contextFor(ctx), ids)
}

// Find queries read models within the transaction context.
func (tr *TxRepository[T]) Find(ctx context.Context, query mink.Query) ([]*T, error) {
	return tr.repo.Find(tr.contextFor(ctx), query)
}

// FindOne returns the first matching read model within the transaction context.
func (tr *TxRepository[T]) FindOne(ctx context.Context, query mink.Query) (*T, error) {
	return tr.repo.FindOne(tr.contextFor(ctx), query)
}

// Count returns the number of matching read models within the transaction context.
func (tr *TxRepository[T]) Count(ctx context.Context, query mink.Query) (int64, error) {
	return tr.repo.Count(tr.contextFor(ctx), query)
}

// Insert creates a read model within the transaction context.
func (tr *TxRepository[T]) Insert(ctx context.Context, model *T) error {
	return tr.repo.Insert(tr.contextFor(ctx), model)
}

// Update modifies a read model within the transaction context.
func (tr *TxRepository[T]) Update(ctx context.Context, id string, updateFn func(*T)) error {
	return tr.repo.Update(tr.contextFor(ctx), id, updateFn)
}

// Upsert creates or updates a read model within the transaction context.
func (tr *TxRepository[T]) Upsert(ctx context.Context, model *T) error {
	return tr.repo.Upsert(tr.contextFor(ctx), model)
}

// Delete removes a read model within the transaction context.
func (tr *TxRepository[T]) Delete(ctx context.Context, id string) error {
	return tr.repo.Delete(tr.contextFor(ctx), id)
}

// DeleteMany removes matching read models within the transaction context.
func (tr *TxRepository[T]) DeleteMany(ctx context.Context, query mink.Query) (int64, error) {
	return tr.repo.DeleteMany(tr.contextFor(ctx), query)
}

// Clear removes all read models within the transaction context.
func (tr *TxRepository[T]) Clear(ctx context.Context) error {
	return tr.repo.Clear(tr.contextFor(ctx))
}

// Exists checks if a read model exists within the transaction context.
func (tr *TxRepository[T]) Exists(ctx context.Context, id string) (bool, error) {
	return tr.repo.Exists(tr.contextFor(ctx), id)
}

// GetAll returns all read models within the transaction context.
func (tr *TxRepository[T]) GetAll(ctx context.Context) ([]*T, error) {
	return tr.repo.GetAll(tr.contextFor(ctx))
}

// CollectionName returns the MongoDB collection name.
func (r *MongoRepository[T]) CollectionName() string {
	return r.config.collection
}

// DatabaseName returns the MongoDB database name.
func (r *MongoRepository[T]) DatabaseName() string {
	return r.config.database
}

func (r *MongoRepository[T]) scanModels(ctx context.Context, cursor *mongo.Cursor) ([]*T, error) {
	var results []*T
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		model, err := r.modelToStruct(doc)
		if err != nil {
			return nil, err
		}
		results = append(results, model)
	}
	return results, cursor.Err()
}

func (r *MongoRepository[T]) structToDocument(model *T) (bson.M, error) {
	if model == nil {
		return nil, fmt.Errorf("mink/mongodb/readmodel: model is nil")
	}
	val := reflect.ValueOf(model).Elem()
	doc := bson.M{}
	for _, field := range r.model.fields {
		value := val.FieldByIndex(field.structIndex)
		if !value.CanInterface() {
			continue
		}
		raw := value.Interface()
		if field.primaryKey {
			doc["_id"] = fmt.Sprint(raw)
		}
		doc[field.name] = raw
	}
	if doc["_id"] == nil || doc["_id"] == "" {
		return nil, fmt.Errorf("mink/mongodb/readmodel: primary key is empty")
	}
	return doc, nil
}

func (r *MongoRepository[T]) modelToStruct(doc bson.M) (*T, error) {
	model := new(T)
	val := reflect.ValueOf(model).Elem()
	for _, field := range r.model.fields {
		raw, ok := doc[field.name]
		if !ok && field.primaryKey {
			raw = doc["_id"]
			ok = true
		}
		if !ok {
			continue
		}
		if err := setReflectValue(val.FieldByIndex(field.structIndex), raw); err != nil {
			return nil, fmt.Errorf("mink/mongodb/readmodel: failed to set %s: %w", field.name, err)
		}
	}
	return model, nil
}

func (r *MongoRepository[T]) buildFilter(filters []mink.Filter) bson.M {
	if len(filters) == 0 {
		return bson.M{}
	}
	conditions := make([]bson.M, 0, len(filters))
	for _, filter := range filters {
		field := r.model.fieldName(filter.Field)
		if field == "" {
			continue
		}
		conditions = append(conditions, mongoCondition(field, filter.Op, filter.Value))
	}
	if len(conditions) == 0 {
		return bson.M{}
	}
	if len(conditions) == 1 {
		return conditions[0]
	}
	return bson.M{"$and": conditions}
}

func (r *MongoRepository[T]) findOptions(query mink.Query) (*options.FindOptionsBuilder, error) {
	if query.Limit < 0 {
		return nil, fmt.Errorf("mink/mongodb/readmodel: limit must be non-negative, got %d", query.Limit)
	}
	if query.Offset < 0 {
		return nil, fmt.Errorf("mink/mongodb/readmodel: offset must be non-negative, got %d", query.Offset)
	}
	opts := options.Find()
	if query.Limit > 0 {
		opts.SetLimit(int64(query.Limit))
	}
	if query.Offset > 0 {
		opts.SetSkip(int64(query.Offset))
	}
	sort := bson.D{}
	for _, order := range query.OrderBy {
		field := r.model.fieldName(order.Field)
		if field == "" {
			continue
		}
		dir := 1
		if order.Desc {
			dir = -1
		}
		sort = append(sort, bson.E{Key: field, Value: dir})
	}
	if len(sort) > 0 {
		opts.SetSort(sort)
	}
	return opts, nil
}

type readModelInfo struct {
	collection string
	fields     []readModelField
}

type readModelField struct {
	structIndex []int
	goName      string
	name        string
	primaryKey  bool
	index       bool
	unique      bool
}

func buildReadModelInfo[T any](collection, idField string) (readModelInfo, error) {
	var sample T
	typ := reflect.TypeOf(sample)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return readModelInfo{}, fmt.Errorf("mink/mongodb/readmodel: type must be a struct, got %s", typ.Kind())
	}
	if collection == "" {
		collection = toSnakeCase(typ.Name())
	}
	info := readModelInfo{collection: collection}

	hasPK := false
	idIdx := -1
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		rmField, skip := parseReadModelField(field)
		if skip {
			continue
		}
		rmField.structIndex = field.Index
		rmField.goName = field.Name
		if rmField.primaryKey {
			hasPK = true
		}
		if field.Name == idField {
			idIdx = len(info.fields)
		}
		info.fields = append(info.fields, rmField)
	}
	if len(info.fields) == 0 {
		return readModelInfo{}, fmt.Errorf("mink/mongodb/readmodel: no exported fields")
	}
	if !hasPK {
		if idIdx >= 0 {
			info.fields[idIdx].primaryKey = true
		} else {
			info.fields[0].primaryKey = true
		}
	}
	return info, nil
}

func parseReadModelField(field reflect.StructField) (readModelField, bool) {
	result := readModelField{name: toSnakeCase(field.Name)}
	tag := field.Tag.Get("mink")
	if tag == "-" {
		return result, true
	}
	if tag != "" {
		parts := strings.Split(tag, ",")
		if parts[0] != "" {
			result.name = parts[0]
		}
		for _, part := range parts[1:] {
			switch {
			case part == "pk":
				result.primaryKey = true
			case part == "index":
				result.index = true
			case part == "unique":
				result.index = true
				result.unique = true
			}
		}
	}
	return result, false
}

func (i readModelInfo) fieldName(name string) string {
	for _, field := range i.fields {
		if field.name == name || field.goName == name || toSnakeCase(field.goName) == name || toSnakeCase(name) == field.name {
			return field.name
		}
	}
	return ""
}

func mongoCondition(field string, op mink.FilterOp, value interface{}) bson.M {
	switch op {
	case mink.FilterOpEq:
		return bson.M{field: value}
	case mink.FilterOpNe:
		return bson.M{field: bson.M{"$ne": value}}
	case mink.FilterOpGt:
		return bson.M{field: bson.M{"$gt": value}}
	case mink.FilterOpGte:
		return bson.M{field: bson.M{"$gte": value}}
	case mink.FilterOpLt:
		return bson.M{field: bson.M{"$lt": value}}
	case mink.FilterOpLte:
		return bson.M{field: bson.M{"$lte": value}}
	case mink.FilterOpIn:
		return bson.M{field: bson.M{"$in": toAnySlice(value)}}
	case mink.FilterOpNotIn:
		return bson.M{field: bson.M{"$nin": toAnySlice(value)}}
	case mink.FilterOpLike:
		return bson.M{field: bson.M{"$regex": likeToRegex(fmt.Sprint(value))}}
	case mink.FilterOpIsNull:
		return bson.M{field: nil}
	case mink.FilterOpIsNotNull:
		return bson.M{field: bson.M{"$ne": nil}}
	case mink.FilterOpContains:
		return bson.M{field: value}
	case mink.FilterOpBetween:
		values := toAnySlice(value)
		if len(values) == 2 {
			return bson.M{field: bson.M{"$gte": values[0], "$lte": values[1]}}
		}
	}
	return bson.M{}
}

func toAnySlice(value interface{}) []interface{} {
	if value == nil {
		return nil
	}
	if values, ok := value.([]interface{}); ok {
		return values
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return []interface{}{value}
	}
	values := make([]interface{}, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		values[i] = rv.Index(i).Interface()
	}
	return values
}

func likeToRegex(pattern string) string {
	quoted := regexpQuote(pattern)
	quoted = strings.ReplaceAll(quoted, "%", ".*")
	quoted = strings.ReplaceAll(quoted, "_", ".")
	return "^" + quoted + "$"
}

func setReflectValue(dst reflect.Value, raw interface{}) error {
	if !dst.CanSet() {
		return nil
	}
	if raw == nil {
		dst.Set(reflect.Zero(dst.Type()))
		return nil
	}
	if dst.Kind() == reflect.Pointer {
		ptr := reflect.New(dst.Type().Elem())
		if err := setReflectValue(ptr.Elem(), raw); err != nil {
			return err
		}
		dst.Set(ptr)
		return nil
	}

	if t, ok := raw.(bson.DateTime); ok && dst.Type() == reflect.TypeOf(time.Time{}) {
		dst.Set(reflect.ValueOf(t.Time()))
		return nil
	}
	if bin, ok := raw.(bson.Binary); ok && dst.Kind() == reflect.Slice && dst.Type().Elem().Kind() == reflect.Uint8 {
		dst.SetBytes(bin.Data)
		return nil
	}

	rawVal := reflect.ValueOf(raw)
	if rawVal.Type().AssignableTo(dst.Type()) {
		dst.Set(rawVal)
		return nil
	}
	if rawVal.Type().ConvertibleTo(dst.Type()) {
		dst.Set(rawVal.Convert(dst.Type()))
		return nil
	}
	if isNumericKind(dst.Kind()) && isNumericKind(rawVal.Kind()) {
		return setNumeric(dst, rawVal)
	}

	data, err := bson.Marshal(bson.M{"v": raw})
	if err != nil {
		return err
	}
	holder := reflect.New(structTypeFor(dst.Type()))
	if err := bson.Unmarshal(data, holder.Interface()); err != nil {
		return err
	}
	dst.Set(holder.Elem().Field(0))
	return nil
}

func isNumericKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func setNumeric(dst, raw reflect.Value) error {
	switch dst.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		dst.SetInt(toInt64(raw))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		dst.SetUint(uint64(toInt64(raw)))
	case reflect.Float32, reflect.Float64:
		dst.SetFloat(toFloat64(raw))
	}
	return nil
}

func toInt64(v reflect.Value) int64 {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(v.Uint())
	case reflect.Float32, reflect.Float64:
		return int64(v.Float())
	default:
		return 0
	}
}

func toFloat64(v reflect.Value) float64 {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint())
	case reflect.Float32, reflect.Float64:
		return v.Float()
	default:
		return 0
	}
}

func structTypeFor(fieldType reflect.Type) reflect.Type {
	return reflect.StructOf([]reflect.StructField{{
		Name: "V",
		Type: fieldType,
		Tag:  `bson:"v"`,
	}})
}

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
			if isLower(prev) || isDigit(prev) || (isUpper(prev) && next != 0 && isLower(next)) {
				result.WriteByte('_')
			}
		}
		result.WriteRune(r)
	}
	return strings.ToLower(result.String())
}
