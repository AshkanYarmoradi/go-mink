# Makefile for go-mink
#
# All test infrastructure is defined in docker-compose.test.yml
# This is the single source of truth for both local and CI environments.

.PHONY: all build test test-unit test-coverage lint fmt clean help
.PHONY: infra-up infra-down infra-logs infra-ps

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

# Run unit tests only (no infrastructure required)
test-unit:
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
	@echo "  make test          - Run all tests (auto-starts infrastructure)"
	@echo "  make test-unit     - Run unit tests only (no infrastructure)"
	@echo "  make test-coverage - Run tests with coverage report"
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
