// Package containers provides connection helpers for integration testing
// against infrastructure that is already running.
//
// Despite the package name, it does NOT provision Docker containers. It assumes
// the test infrastructure (PostgreSQL) has already been started out of band —
// typically via the repository's docker-compose.test.yml (see `make infra-up`)
// or by an equivalent service in CI — and provides convenience helpers to
// connect to it, create/drop isolated test schemas, and wire up an integration
// test harness. When the expected instance is not reachable, StartPostgres
// skips the calling test rather than failing it, so unit-only environments stay
// green.
//
// There is intentionally no StartKafka (or other broker) helper here: Kafka
// integration tests connect directly using the TEST_KAFKA_BROKERS environment
// variable and skip themselves when it is unset. If you need real
// container provisioning (lifecycle managed from within the test), add
// testcontainers-go as a dependency; this package deliberately avoids that
// dependency to keep the default build lightweight.
package containers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Validation errors.
var (
	// ErrInvalidSchemaPrefix indicates the schema prefix is not usable: it must be
	// a plain identifier (letters, digits and underscores, not starting with a
	// digit) of at most MaxSchemaPrefixBytes bytes, so that the complete
	// "<prefix>_<UnixNano>" name CreateSchema builds from it fits PostgreSQL's
	// 63-byte identifier limit. A longer prefix would make the server silently
	// truncate the generated name (collapsing successive schemas onto one) and
	// DropSchema would then refuse the untruncated name, leaking the schema.
	ErrInvalidSchemaPrefix = errors.New("containers: schema prefix must start with a letter or underscore, contain only alphanumeric characters and underscores, and be at most 43 bytes")

	// ErrInvalidSchemaName indicates a schema name is not a plain PostgreSQL
	// identifier: it must start with a letter or underscore, contain only ASCII
	// letters, digits and underscores, and be at most MaxSchemaNameBytes (63)
	// bytes — PostgreSQL silently truncates longer identifiers, so a 64-byte name
	// would otherwise be dropped as whichever 63-byte schema shares its head.
	// DropSchema and DropSchemaUnsafe return it (without touching the database)
	// for any other value.
	ErrInvalidSchemaName = errors.New("containers: schema name must start with a letter or underscore, contain only alphanumeric characters and underscores, and be at most 63 bytes")

	// ErrSchemaNotOwned indicates that DropSchema was handed a schema name this
	// PostgresContainer instance did not create: a well-formed identifier is not
	// enough, the name must have been returned by CreateSchema on the SAME
	// instance (ownership is recorded per instance, not per process). "public",
	// another test's schema, or a schema created through a different
	// StartPostgres handle are all refused before the database is contacted, so
	// a test can only ever drop its own schemas. DropSchemaUnsafe is the
	// explicit opt-out for cleaning up a schema that was created elsewhere.
	ErrSchemaNotOwned = errors.New("containers: schema was not created by this container instance; use DropSchemaUnsafe to drop a schema created elsewhere")

	// ErrInvalidPort indicates the port is not valid.
	ErrInvalidPort = errors.New("containers: port must be a valid number between 1 and 65535")

	// ErrEmptyPassword indicates the password is empty.
	ErrEmptyPassword = errors.New("containers: password cannot be empty")
)

// identifierRegex validates PostgreSQL identifiers (schema names, prefixes):
// an ASCII letter or underscore followed by ASCII letters, digits or
// underscores. Both CreateSchema (prefix) and DropSchema (full name) enforce it
// before the identifier is interpolated into DDL, so the quoting in
// quoteIdentifier is defence in depth rather than the only barrier.
var identifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

const (
	// MaxSchemaNameBytes is the longest schema name DropSchema accepts: 63 bytes,
	// PostgreSQL's identifier limit (NAMEDATALEN-1). The server does not reject a
	// longer identifier, it silently truncates it, so without this bound a
	// caller-supplied 64-byte name would be dropped as whichever existing schema
	// shares its first 63 bytes. Longer names fail with ErrInvalidSchemaName.
	MaxSchemaNameBytes = 63

	// schemaSuffixBytes is the length of the "_<UnixNano>" suffix CreateSchema
	// appends to a prefix: an underscore plus the 19 decimal digits a Unix
	// nanosecond timestamp has from 2001 until int64 overflows in 2262.
	schemaSuffixBytes = 1 + 19

	// MaxSchemaPrefixBytes is the longest prefix CreateSchema (and therefore
	// WithSchemaPrefix) accepts: 43 bytes, so that "<prefix>_<UnixNano>" always
	// fits MaxSchemaNameBytes and DropSchema accepts every name CreateSchema
	// returns. Longer prefixes fail with ErrInvalidSchemaPrefix.
	MaxSchemaPrefixBytes = MaxSchemaNameBytes - schemaSuffixBytes
)

// PostgresContainer represents a PostgreSQL test container.
//
// Each instance remembers the schema names its CreateSchema has returned and
// DropSchema will only drop those (see ErrSchemaNotOwned), so hold on to the
// handle that created a schema to clean it up. The zero value is usable; the
// ownership set is created lazily and guarded by a mutex, so one handle may be
// shared by parallel subtests.
type PostgresContainer struct {
	Host     string
	Port     string
	Database string
	User     string
	Password string
	connStr  string

	// mu guards owned. owned holds every schema name CreateSchema has returned
	// on this instance; DropSchema refuses any name that is not in it.
	mu    sync.Mutex
	owned map[string]struct{}
}

// PostgresOption configures a PostgreSQL container.
type PostgresOption func(*postgresConfig)

type postgresConfig struct {
	image    string
	database string
	user     string
	password string
	port     string
}

// WithPostgresImage sets the PostgreSQL Docker image.
func WithPostgresImage(image string) PostgresOption {
	return func(c *postgresConfig) {
		c.image = image
	}
}

// WithPostgresDatabase sets the database name.
func WithPostgresDatabase(database string) PostgresOption {
	return func(c *postgresConfig) {
		c.database = database
	}
}

// WithPostgresUser sets the database user.
func WithPostgresUser(user string) PostgresOption {
	return func(c *postgresConfig) {
		c.user = user
	}
}

// WithPostgresPassword sets the database password.
func WithPostgresPassword(password string) PostgresOption {
	return func(c *postgresConfig) {
		c.password = password
	}
}

// WithPostgresPort sets the host port.
func WithPostgresPort(port string) PostgresOption {
	return func(c *postgresConfig) {
		c.port = port
	}
}

// getEnvOrDefault returns environment variable value or default.
func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// defaultPostgresConfig returns the default PostgreSQL configuration.
// Configuration is read from environment variables with fallback to defaults
// that match docker-compose.test.yml for seamless local testing.
//
// Environment variables:
//   - POSTGRES_IMAGE: Docker image (default: postgres:17)
//   - POSTGRES_DB or TEST_POSTGRES_DB: Database name (default: mink_test)
//   - POSTGRES_USER or TEST_POSTGRES_USER: Username (default: postgres)
//   - POSTGRES_PASSWORD or TEST_POSTGRES_PASSWORD: Password (default: postgres)
//   - POSTGRES_PORT or TEST_POSTGRES_PORT: Port (default: 5432)
//
// TEST-ONLY CREDENTIALS: with nothing set, these defaults assemble the DSN
// postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable — the
// throw-away database started by docker-compose.test.yml. The user/password
// pair is deliberately well known and is meant solely for a disposable local
// or CI instance; never reuse it for, or point this helper at, a shared or
// production server. Redirect the helper with the per-field variables above
// or with PostgresOption values. This package does NOT read TEST_DATABASE_URL
// (a complete DSN): that variable is honoured by testutil.DefaultConfig and by
// integration tests that read it directly, so when you override it you should
// set the matching POSTGRES_* / TEST_POSTGRES_* variables as well to keep the
// two helpers pointed at the same database.
func defaultPostgresConfig() *postgresConfig {
	return &postgresConfig{
		image:    getEnvOrDefault("POSTGRES_IMAGE", "postgres:17"),
		database: getEnvOrDefault("POSTGRES_DB", getEnvOrDefault("TEST_POSTGRES_DB", "mink_test")),
		user:     getEnvOrDefault("POSTGRES_USER", getEnvOrDefault("TEST_POSTGRES_USER", "postgres")),
		password: getEnvOrDefault("POSTGRES_PASSWORD", getEnvOrDefault("TEST_POSTGRES_PASSWORD", "postgres")),
		port:     getEnvOrDefault("POSTGRES_PORT", getEnvOrDefault("TEST_POSTGRES_PORT", "5432")),
	}
}

// StartPostgres connects to an already-running PostgreSQL instance and returns
// a handle for use in integration tests. It does NOT provision a container:
// the instance is expected to be running already (for example via
// docker-compose.test.yml / `make infra-up`, or a CI service). Connection
// settings default to those in docker-compose.test.yml and can be overridden
// with PostgresOption values or the documented environment variables.
//
// If the instance is not reachable within the readiness timeout, the calling
// test is skipped (t.Skip) rather than failed, so the suite still passes in
// environments without the infrastructure.
//
// The built-in defaults (postgres/postgres on localhost:5432, database
// mink_test, sslmode=disable) are test-only credentials for the disposable
// docker-compose database; see defaultPostgresConfig for the environment
// variables that override them. StartPostgres does not consult
// TEST_DATABASE_URL — that full-DSN variable drives testutil.DefaultConfig.
//
// StartKafka (kafka.go) is the analogous helper for Kafka: it connects via
// TEST_KAFKA_BROKERS and skips when it is unset/unreachable. Neither helper
// provisions real containers from within tests; to do that add testcontainers-go,
// a dependency this package avoids on purpose.
func StartPostgres(t *testing.T, opts ...PostgresOption) *PostgresContainer {
	t.Helper()

	cfg := defaultPostgresConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	container := &PostgresContainer{
		Host:     "localhost",
		Port:     cfg.port,
		Database: cfg.database,
		User:     cfg.user,
		Password: cfg.password,
	}
	container.connStr = container.ConnectionString()

	// Wait for database to be ready
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := waitForPostgres(ctx, container.connStr); err != nil {
		t.Skipf("PostgreSQL not available (run docker-compose -f docker-compose.test.yml up -d): %v", err)
	}

	return container
}

// ConnectionString returns the PostgreSQL connection string.
func (c *PostgresContainer) ConnectionString() string {
	if c.connStr != "" {
		return c.connStr
	}
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		c.User, c.Password, c.Host, c.Port, c.Database,
	)
}

// DB returns a database connection.
func (c *PostgresContainer) DB(ctx context.Context) (*sql.DB, error) {
	db, err := sql.Open("pgx", c.ConnectionString())
	if err != nil {
		return nil, fmt.Errorf("containers: failed to open connection: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("containers: failed to ping database: %w", err)
	}

	return db, nil
}

// MustDB returns a database connection or panics.
func (c *PostgresContainer) MustDB(ctx context.Context) *sql.DB {
	db, err := c.DB(ctx)
	if err != nil {
		panic(err)
	}
	return db
}

// CreateSchema creates a unique test schema named "<prefix>_<UnixNano>".
//
// The prefix must be a plain identifier of at most MaxSchemaPrefixBytes (43)
// bytes; anything else returns ErrInvalidSchemaPrefix before the database is
// contacted. The bound guarantees the generated name never exceeds PostgreSQL's
// 63-byte identifier limit, so the server cannot silently truncate the
// uniqueness suffix away (which would make successive calls resolve to the same
// schema) and DropSchema accepts every name this method returns.
//
// The returned name is recorded as owned by this instance (for the lifetime of
// the instance, a successful drop does not forget it), which is what entitles a
// later DropSchema on the same instance to drop it. A name is recorded only
// after the CREATE SCHEMA statement succeeds.
func (c *PostgresContainer) CreateSchema(ctx context.Context, db *sql.DB, prefix string) (string, error) {
	if err := validateSchemaPrefix(prefix); err != nil {
		return "", err
	}
	schema := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	_, err := db.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema)))
	if err != nil {
		return "", fmt.Errorf("containers: failed to create schema: %w", err)
	}
	c.recordSchema(schema)
	return schema, nil
}

// DropSchema drops a test schema (and everything in it, CASCADE) that this
// PostgresContainer instance created.
//
// Two checks run before the database is contacted, in this order:
//
//  1. The schema name is interpolated into DDL, so it is validated: it must be
//     a plain identifier matching ^[A-Za-z_][A-Za-z0-9_]*$ of at most
//     MaxSchemaNameBytes (63) bytes — which every name returned by CreateSchema
//     satisfies. Any other value (empty, leading digit, quotes, semicolons,
//     whitespace, non-ASCII, or 64+ bytes) returns ErrInvalidSchemaName. The
//     character rule stops a caller-supplied name from escaping the quoted
//     identifier; the length rule stops PostgreSQL from silently truncating a
//     longer name onto an existing 63-byte schema.
//  2. The name must have been returned by CreateSchema on this same instance;
//     otherwise ErrSchemaNotOwned is returned. A valid identifier alone proves
//     nothing about whose schema it is — "public", a schema created by another
//     test, or one created through a different StartPostgres handle are all
//     refused — so a test can only ever drop the schemas it created.
//
// Together the two rules make it impossible to drop anything other than a
// schema this instance created. Dropping an owned schema is idempotent (DROP
// SCHEMA IF EXISTS), so cleanup may safely run more than once. To drop a
// schema that was created elsewhere, opt in explicitly with DropSchemaUnsafe.
func (c *PostgresContainer) DropSchema(ctx context.Context, db *sql.DB, schema string) error {
	if err := validateSchemaName(schema); err != nil {
		return err
	}
	if !c.ownsSchema(schema) {
		return ErrSchemaNotOwned
	}
	return c.dropSchema(ctx, db, schema)
}

// DropSchemaUnsafe drops a schema (and everything in it, CASCADE) WITHOUT the
// ownership check DropSchema applies, for the rare case of cleaning up a schema
// that was created elsewhere — by another PostgresContainer instance, by a
// previous process that crashed before its cleanup ran, or by hand.
//
// The identifier validation is unchanged: the name must be a plain identifier
// of at most MaxSchemaNameBytes bytes or ErrInvalidSchemaName is returned
// before the database is contacted. Everything else is the caller's
// responsibility: nothing stops this method from dropping "public" or another
// running test's schema, so prefer DropSchema with the handle that created the
// schema whenever you have it.
func (c *PostgresContainer) DropSchemaUnsafe(ctx context.Context, db *sql.DB, schema string) error {
	if err := validateSchemaName(schema); err != nil {
		return err
	}
	return c.dropSchema(ctx, db, schema)
}

// dropSchema executes the DROP SCHEMA statement. Callers must have validated
// the name (validateSchemaName) first; quoteIdentifier is only defence in depth.
func (c *PostgresContainer) dropSchema(ctx context.Context, db *sql.DB, schema string) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(schema)))
	return err
}

// recordSchema marks schema as created by (and therefore droppable through)
// this instance.
func (c *PostgresContainer) recordSchema(schema string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owned == nil {
		c.owned = make(map[string]struct{})
	}
	c.owned[schema] = struct{}{}
}

// ownsSchema reports whether CreateSchema on this instance returned schema.
func (c *PostgresContainer) ownsSchema(schema string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.owned[schema]
	return ok
}

// waitForPostgres waits for PostgreSQL to be ready.
func waitForPostgres(ctx context.Context, connStr string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			db, err := sql.Open("pgx", connStr)
			if err != nil {
				continue
			}
			err = db.PingContext(ctx)
			_ = db.Close()
			if err == nil {
				return nil
			}
		}
	}
}

// quoteIdentifier quotes a PostgreSQL identifier with double quotes, doubling
// any embedded double quote (" -> "") so the value can never terminate the
// quoted identifier early. Callers still validate the name against
// identifierRegex first; the escaping here is a second, independent barrier.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// validateSchemaPrefix validates a schema prefix for use in test schema names:
// a non-empty identifier (identifierRegex) of at most MaxSchemaPrefixBytes
// bytes, so that the "<prefix>_<UnixNano>" name built from it always passes
// validateSchemaName. Returns ErrInvalidSchemaPrefix otherwise.
func validateSchemaPrefix(prefix string) error {
	if prefix == "" || len(prefix) > MaxSchemaPrefixBytes || !identifierRegex.MatchString(prefix) {
		return ErrInvalidSchemaPrefix
	}
	return nil
}

// validateSchemaName validates a full schema name before it is interpolated
// into DDL (see DropSchema). It accepts exactly the identifiers matched by
// identifierRegex that are at most MaxSchemaNameBytes bytes long, and returns
// ErrInvalidSchemaName for anything else. The length check matters because
// PostgreSQL truncates, rather than rejects, longer identifiers: a 64-byte name
// would otherwise address the existing schema that shares its first 63 bytes.
func validateSchemaName(schema string) error {
	if schema == "" || len(schema) > MaxSchemaNameBytes || !identifierRegex.MatchString(schema) {
		return ErrInvalidSchemaName
	}
	return nil
}

// =============================================================================
// Integration Test Helper
// =============================================================================

// IntegrationTest provides a complete integration test environment.
type IntegrationTest struct {
	t         *testing.T
	ctx       context.Context
	container *PostgresContainer
	db        *sql.DB
	schema    string
}

// IntegrationTestOption configures an integration test.
type IntegrationTestOption func(*integrationTestConfig)

type integrationTestConfig struct {
	schemaPrefix string
	timeout      time.Duration
}

// WithSchemaPrefix sets the schema prefix. It must satisfy CreateSchema's
// rules (a plain identifier of at most MaxSchemaPrefixBytes bytes) or
// NewIntegrationTest fails the test with ErrInvalidSchemaPrefix.
func WithSchemaPrefix(prefix string) IntegrationTestOption {
	return func(c *integrationTestConfig) {
		c.schemaPrefix = prefix
	}
}

// WithTimeout sets the test timeout.
func WithTimeout(timeout time.Duration) IntegrationTestOption {
	return func(c *integrationTestConfig) {
		c.timeout = timeout
	}
}

// NewIntegrationTest creates a new integration test environment.
func NewIntegrationTest(t *testing.T, opts ...IntegrationTestOption) *IntegrationTest {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	cfg := &integrationTestConfig{
		schemaPrefix: "test",
		timeout:      30 * time.Second,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	t.Cleanup(cancel)

	container := StartPostgres(t)

	db, err := container.DB(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to database: %v", err)
	}

	schema, err := container.CreateSchema(ctx, db, cfg.schemaPrefix)
	if err != nil {
		_ = db.Close()
		t.Fatalf("Failed to create schema: %v", err)
	}

	it := &IntegrationTest{
		t:         t,
		ctx:       ctx,
		container: container,
		db:        db,
		schema:    schema,
	}

	t.Cleanup(func() {
		if err := container.DropSchema(context.Background(), db, schema); err != nil {
			t.Logf("Warning: failed to drop schema %s: %v", schema, err)
		}
		_ = db.Close()
	})

	return it
}

// Context returns the test context.
func (it *IntegrationTest) Context() context.Context {
	return it.ctx
}

// DB returns the database connection.
func (it *IntegrationTest) DB() *sql.DB {
	return it.db
}

// Schema returns the test schema name.
func (it *IntegrationTest) Schema() string {
	return it.schema
}

// Container returns the PostgreSQL container.
func (it *IntegrationTest) Container() *PostgresContainer {
	return it.container
}

// ConnectionString returns the connection string with schema.
func (it *IntegrationTest) ConnectionString() string {
	return it.container.ConnectionString() + "&search_path=" + it.schema
}

// Exec executes a SQL statement.
func (it *IntegrationTest) Exec(query string, args ...interface{}) {
	it.t.Helper()
	_, err := it.db.ExecContext(it.ctx, query, args...)
	if err != nil {
		it.t.Fatalf("Failed to execute SQL: %v", err)
	}
}

// Query executes a SQL query.
func (it *IntegrationTest) Query(query string, args ...interface{}) *sql.Rows {
	it.t.Helper()
	rows, err := it.db.QueryContext(it.ctx, query, args...)
	if err != nil {
		it.t.Fatalf("Failed to execute query: %v", err)
	}
	return rows
}

// =============================================================================
// Test Fixture for Full Stack Testing
// =============================================================================

// FullStackTest provides complete end-to-end test infrastructure.
type FullStackTest struct {
	*IntegrationTest
}

// NewFullStackTest creates a new full stack test environment.
func NewFullStackTest(t *testing.T) *FullStackTest {
	t.Helper()
	return &FullStackTest{
		IntegrationTest: NewIntegrationTest(t, WithSchemaPrefix("fullstack")),
	}
}

// SetupMinkSchema creates the mink event store schema.
func (fst *FullStackTest) SetupMinkSchema() {
	fst.t.Helper()

	// Create events table
	fst.Exec(`
		CREATE TABLE IF NOT EXISTS events (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			stream_id VARCHAR(255) NOT NULL,
			version BIGINT NOT NULL,
			type VARCHAR(255) NOT NULL,
			data JSONB NOT NULL,
			metadata JSONB DEFAULT '{}',
			global_position BIGSERIAL,
			timestamp TIMESTAMPTZ DEFAULT NOW(),
			UNIQUE(stream_id, version)
		)
	`)

	// Create index for efficient stream queries
	fst.Exec(`CREATE INDEX IF NOT EXISTS idx_events_stream ON events(stream_id, version)`)
	fst.Exec(`CREATE INDEX IF NOT EXISTS idx_events_global ON events(global_position)`)
	fst.Exec(`CREATE INDEX IF NOT EXISTS idx_events_type ON events(type)`)

	// Create checkpoints table
	fst.Exec(`
		CREATE TABLE IF NOT EXISTS checkpoints (
			projection_name VARCHAR(255) PRIMARY KEY,
			position BIGINT NOT NULL,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)
	`)

	// Create idempotency table
	fst.Exec(`
		CREATE TABLE IF NOT EXISTS idempotency_keys (
			key VARCHAR(255) PRIMARY KEY,
			result JSONB NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			expires_at TIMESTAMPTZ
		)
	`)
}
