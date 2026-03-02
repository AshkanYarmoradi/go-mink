# Makefile for go-mink
#
# All test infrastructure is defined in docker-compose.test.yml
# This is the single source of truth for both local and CI environments.

.PHONY: all build test test-unit test-unit-race test-coverage lint fmt clean help
.PHONY: infra-up infra-down infra-logs infra-ps
.PHONY: benchmark benchmark-quick benchmark-adapters benchmark-adapters-pg

# Default target
all: lint test build

#------------------------------------------------------------------------------
# Build
#------------------------------------------------------------------------------

build:
	go build -v ./...

#------------------------------------------------------------------------------
# Testing
#------------------------------------------------------------------------------

# Run unit tests only (no infrastructure required, works on all platforms)
test-unit:
	CGO_ENABLED=0 go test -short -v ./...

# Run unit tests with race detector (requires CGO toolchain: gcc/clang)
test-unit-race:
	go test -short -race -v ./...

# Run all tests (starts infrastructure automatically)
test: infra-up
	TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable" \
	TEST_KAFKA_BROKERS="localhost:9092" \
	go test -race -v ./...

# Run tests with coverage report (excludes examples and test utilities)
test-coverage: infra-up
	TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable" \
	TEST_KAFKA_BROKERS="localhost:9092" \
	go test -race -coverprofile=coverage.out -covermode=atomic \
		$$(go list ./... | grep -v '/examples/' | grep -v '/testing/')
	@echo ""
	@echo "=== Coverage Summary ==="
	@go tool cover -func=coverage.out | grep total
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Full report: coverage.html"

#------------------------------------------------------------------------------
# Benchmarks & Performance (no infrastructure required, uses in-memory adapter)
#------------------------------------------------------------------------------

# Run full benchmark suite: Go benchmarks + 1M-event scale tests
benchmark:
	@echo "=== Running Go Benchmarks ==="
	CGO_ENABLED=0 go test -run='^$$' -bench=. -benchmem -benchtime=3s -count=1 .
	@echo ""
	@echo "=== Running Scale Tests (1M events) ==="
	CGO_ENABLED=0 go test -run='TestScale_' -v -timeout=10m -count=1 .

# Run quick benchmarks only (no scale tests)
benchmark-quick:
	CGO_ENABLED=0 go test -run='^$$' -bench=. -benchmem -benchtime=1s -count=1 .

# Run memory adapter benchmarks (no infrastructure required)
benchmark-adapters:
	@echo "=== Memory Adapter Benchmarks ==="
	CGO_ENABLED=0 go test -run='^$$' -bench=. -benchmem -benchtime=3s -count=1 ./adapters/memory/
	@echo ""
	@echo "=== Memory Adapter Scale Tests ==="
	CGO_ENABLED=0 go test -run='TestAdapterScale' -v -timeout=10m -count=1 ./adapters/memory/

# Run PostgreSQL adapter benchmarks (requires infra)
benchmark-adapters-pg: infra-up
	@echo "=== PostgreSQL Adapter Benchmarks ==="
	TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable" \
	go test -run='^$$' -bench=. -benchmem -benchtime=3s -count=1 ./adapters/postgres/
	@echo ""
	@echo "=== PostgreSQL Adapter Scale Tests ==="
	TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable" \
	go test -run='TestAdapterScale' -v -timeout=10m -count=1 ./adapters/postgres/

#------------------------------------------------------------------------------
# Test Infrastructure (docker-compose.test.yml is the single source of truth)
#------------------------------------------------------------------------------

infra-up:
	@echo "Starting test infrastructure..."
	docker compose -f docker-compose.test.yml up -d --wait
	@echo "Infrastructure ready!"

infra-down:
	@echo "Stopping test infrastructure..."
	docker compose -f docker-compose.test.yml down
	@echo "Infrastructure stopped."

infra-logs:
	docker compose -f docker-compose.test.yml logs -f

infra-ps:
	docker compose -f docker-compose.test.yml ps

#------------------------------------------------------------------------------
# Code Quality
#------------------------------------------------------------------------------

lint:
	golangci-lint run ./...

fmt:
	go fmt ./...

#------------------------------------------------------------------------------
# Cleanup
#------------------------------------------------------------------------------

clean: infra-down
	rm -f coverage.out coverage.html
	go clean -cache -testcache

#------------------------------------------------------------------------------
# Help
#------------------------------------------------------------------------------

help:
	@echo "go-mink Development Commands"
	@echo ""
	@echo "Testing:"
	@echo "  make test-unit      - Run unit tests (no infra, works on all platforms)"
	@echo "  make test-unit-race - Run unit tests with race detector (requires gcc)"
	@echo "  make test           - Run all tests (auto-starts infrastructure)"
	@echo "  make test-coverage  - Run tests with coverage report"
	@echo ""
	@echo "Benchmarks:"
	@echo "  make benchmark            - Full benchmark suite + 1M-event scale tests"
	@echo "  make benchmark-quick      - Go benchmarks only (fast)"
	@echo "  make benchmark-adapters   - Memory adapter benchmarks (no infra)"
	@echo "  make benchmark-adapters-pg - PostgreSQL adapter benchmarks (needs infra)"
	@echo ""
	@echo "Infrastructure (defined in docker-compose.test.yml):"
	@echo "  make infra-up      - Start test infrastructure"
	@echo "  make infra-down    - Stop test infrastructure"
	@echo "  make infra-logs    - View infrastructure logs"
	@echo "  make infra-ps      - Show infrastructure status"
	@echo ""
	@echo "Code Quality:"
	@echo "  make build         - Build all packages"
	@echo "  make lint          - Run golangci-lint"
	@echo "  make fmt           - Format code"
	@echo ""
	@echo "Cleanup:"
	@echo "  make clean         - Stop infrastructure and clean artifacts"
