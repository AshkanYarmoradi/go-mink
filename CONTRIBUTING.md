# Contributing to go-mink

First off, thank you for considering contributing to go-mink! It's people like you that make go-mink such a great tool.

Following these guidelines helps to communicate that you respect the time of the developers managing and developing this open source project. In return, they should reciprocate that respect in addressing your issue, assessing changes, and helping you finalize your pull requests.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [I Have a Question](#i-have-a-question)
- [I Want To Contribute](#i-want-to-contribute)
  - [Reporting Bugs](#reporting-bugs)
  - [Suggesting Enhancements](#suggesting-enhancements)
  - [Your First Code Contribution](#your-first-code-contribution)
  - [Pull Requests](#pull-requests)
- [Styleguides](#styleguides)
  - [Git Commit Messages](#git-commit-messages)
  - [Go Styleguide](#go-styleguide)

## Code of Conduct

This project and everyone participating in it is governed by the [go-mink Code of Conduct](CODE_OF_CONDUCT.md). By participating, you are expected to uphold this code. Please report unacceptable behavior to the project maintainers.

## I Have a Question

Questions are often better asked in our [Discussions](https://github.com/AshkanYarmoradi/go-mink/discussions) (if enabled) or by opening an Issue with the `question` label.

## I Want To Contribute

### Reporting Bugs

This section guides you through submitting a bug report for go-mink. Following these guidelines helps maintainers and the community understand your report, reproduce the behavior, and find related reports.

**Before Submitting a Bug Report**

*   **Check the [documentation](https://go-mink.dev)** for guides and common questions.
*   **Search the existing Issues** to see if the problem has already been reported.

**How to Submit a Good Bug Report**

Open an Issue and provide the following information:

*   **Use a clear and descriptive title** for the issue to identify the problem.
*   **Describe the exact steps to reproduce the problem** in as many details as possible.
*   **Provide specific examples to demonstrate the steps**. Include links to files or GitHub projects, or copy/pasteable snippets, which you use in those examples.
*   **Describe the behavior you observed after following the steps** and point out what exactly is the problem with that behavior.
*   **Explain which behavior you expected to see instead and why.**
*   **Include details about your configuration and environment**: Go version, OS, database version, etc.

### Suggesting Enhancements

This section guides you through submitting an enhancement suggestion for go-mink, including completely new features and minor improvements to existing functionality.

**How to Submit a Good Enhancement Suggestion**

*   **Use a clear and descriptive title** for the issue to identify the suggestion.
*   **Provide a step-by-step description of the suggested enhancement** in as many details as possible.
*   **Explain why this enhancement would be useful** to most go-mink users.

### Your First Code Contribution

1.  **Fork the repository** on GitHub.
2.  **Clone your fork** locally.
3.  **Create a new branch** from `develop` for your feature or fix.
    ```bash
    git checkout develop
    git pull origin develop
    git checkout -b feature/amazing-feature
    ```
4.  **Make your changes**.
5.  **Run the checks** to make sure everything passes.
    ```bash
    make test-unit   # fast unit tests — no infrastructure required
    make lint        # golangci-lint
    make fmt         # go fmt ./...
    ```
    For the full suite (which spins up PostgreSQL + Kafka via Docker Compose), run `make test`.
6.  **Commit your changes** using descriptive commit messages.

### Branching Strategy & Release Process

We use a two-branch model with automated releases:

```
feature branch ──► develop (RC releases) ──► main (stable releases)
```

*   **`develop`** — Integration branch. All feature branches target `develop` via pull request. Each merge to `develop` automatically creates a release candidate (e.g., `v1.0.4-rc.1`, `v1.0.4-rc.2`).
*   **`main`** — Stable releases only. Merging `develop` into `main` automatically creates the next stable release (e.g., `v1.0.4`).
*   **Feature branches** — Created from `develop`, named `feature/description` or `fix/description`.

**RC (Release Candidate) versions** are published as GitHub pre-releases and indexed on the Go module proxy. Users can opt in:

```bash
go get go-mink.dev@v1.0.4-rc.1
```

The default `go get` (without a version suffix) always returns the latest **stable** release — RC versions are never served as `@latest`.

### Pull Requests

The process described here has several goals:

- Maintain go-mink's quality
- Fix problems that are important to users
- Engage the community in working toward the best possible go-mink

Please follow these steps to have your contribution considered by the maintainers:

1.  Follow all instructions in [the template](.github/PULL_REQUEST_TEMPLATE.md) (if available).
2.  Follow the [styleguides](#styleguides).
3.  After you submit your pull request, verify that all status checks are passing.

## Styleguides

### Git Commit Messages

*   Use the present tense ("Add feature" not "Added feature")
*   Use the imperative mood ("Move cursor to..." not "Moves cursor to...")
*   Limit the first line to 72 characters or less
*   Reference issues and pull requests liberally after the first line
*   Consider using [Conventional Commits](https://www.conventionalcommits.org/)

### Go Styleguide

*   We follow the official [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments).
*   Run `go fmt` before committing.
*   Run `go vet` to catch common errors.
*   Ensure all public functions and types have documentation comments.

## Development Setup

### Prerequisites

- **Go 1.25+** — the module targets Go 1.25; CI builds on Go 1.25 and 1.26.
- **Docker** — used by `make test` to spin up PostgreSQL + Kafka for integration tests.
- **golangci-lint** — for `make lint` ([installation guide](https://golangci-lint.run/welcome/install/)).

Infrastructure for integration tests is managed for you via `docker-compose.test.yml` — the Makefile starts and stops it automatically, so you rarely need to touch Docker directly.

### Common Commands

```bash
make build           # go build ./...
make test-unit       # unit tests only (no infra, CGO_ENABLED=0 — works everywhere)
make test-unit-race  # unit tests with the race detector (needs gcc/clang)
make test            # full suite — auto-starts PostgreSQL + Kafka
make test-e2e        # end-to-end suites only (TestE2E_*) against real PostgreSQL + Kafka
make lint            # golangci-lint run ./...
make fmt             # go fmt ./...
make test-coverage   # coverage report (excludes examples/ and testing/)
make help            # list every target
```

### End-to-end suite

The `e2e_*_test.go` files (package `mink_test`) exercise each feature through the full stack
against real infrastructure — command bus → PostgreSQL store → projections; store → outbox →
Kafka/webhook; field encryption ciphertext-at-rest; and the GDPR erasure/export lifecycle. They
build on `testing/containers` (`StartPostgres`, `StartKafka`, per-test isolated schemas) and
self-skip under `-short` or when their env vars are unset:

- **PostgreSQL** — `TEST_DATABASE_URL` (required by every E2E suite).
- **Kafka** — `TEST_KAFKA_BROKERS` (the outbox→Kafka suite; others don't need it).
- **Cloud providers (opt-in, not required in CI)** — the SNS, KMS, and Vault suites skip unless
  their endpoint env var is set. Any AWS-compatible emulator works (no license needed):
  - **SNS** — `TEST_SNS_ENDPOINT` (e.g. `docker run -d -p 4100:4100 pafortin/goaws` → `http://localhost:4100`)
  - **KMS** — `TEST_KMS_ENDPOINT` (e.g. `docker run -d -p 8087:8080 nsmithuk/local-kms` → `http://localhost:8087`)
  - **Vault** — `VAULT_ADDR` (+ `VAULT_TOKEN`, default `root`) (e.g. a `hashicorp/vault` dev container on `http://localhost:8200`; the test provisions the transit key itself)

Run the core suites with `make test-e2e` (auto-starts PostgreSQL + Kafka); set the cloud env vars
above to also run those. Because every suite self-skips, `make test-unit` stays green everywhere.

To run a single test (integration tests need infra up — `make infra-up`):

```bash
TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/mink_test?sslmode=disable" \
  go test -v -run TestName ./path/to/pkg/...
```

Integration tests skip themselves automatically under `-short`, or when `TEST_DATABASE_URL` / `TEST_KAFKA_BROKERS` are unset — so `make test-unit` is safe to run anywhere.

### Test Coverage

**CI enforces 90% coverage** (excluding `examples/` and `testing/`), so new features are expected to ship with tests. Check coverage locally with `make test-coverage`; overall coverage is tracked on [Codecov](https://codecov.io/gh/AshkanYarmoradi/go-mink) and [SonarCloud](https://sonarcloud.io/summary/new_code?id=AshkanYarmoradi_go-mink).

## Project Layout

A quick map to help you navigate the codebase:

| Path | What lives here |
|------|-----------------|
| `*.go` (root, package `mink`) | Core types: event store, command bus, projections, sagas, outbox, encryption, GDPR toolkit, versioning |
| `adapters/` | Storage adapters — `postgres/` (production), `memory/` (testing), and the shared interfaces in `adapter.go` |
| `encryption/` | The `Provider` interface plus `local/`, `kms/`, and `vault/` implementations |
| `outbox/` | Outbox publishers — `webhook/`, `kafka/`, `sns/` |
| `serializer/` | Alternative serializers — `msgpack/`, `protobuf/` |
| `middleware/` | `metrics/` (Prometheus) and `tracing/` (OpenTelemetry) |
| `testing/` | Test utilities — BDD fixtures, assertions, projection/saga harnesses, containers |
| `cli/`, `cmd/mink/` | The `mink` command-line tool |
| `examples/` | Runnable example projects (each with its own README) |
| `website/` | The Docusaurus documentation site ([go-mink.dev](https://go-mink.dev)) |

Adding storage support means implementing the interfaces in [`adapters/adapter.go`](adapters/adapter.go) — see the [Adapters guide](https://go-mink.dev/docs/core/adapters).

## Good First Contributions

Looking for a place to start? Any of these are genuinely useful:

- 📝 Improve a documentation page or an example README.
- 🧪 Add a test that covers an edge case.
- 📊 Add a benchmark using the shared helpers in `adapters/common.go`.
- 🔌 Implement an `EventStoreAdapter` for another database.
- 🐛 Pick up a [good first issue](https://github.com/AshkanYarmoradi/go-mink/labels/good%20first%20issue).

Not sure where your idea fits? Open a [Discussion](https://github.com/AshkanYarmoradi/go-mink/discussions) — we're happy to help you get started.

Thank you for contributing! 🦫
