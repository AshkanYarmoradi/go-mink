---
layout: default
title: Architecture Decision Records
nav_order: 12
has_children: true
permalink: /docs/adr
---

# Architecture Decision Records
{: .no_toc }

Documenting significant architectural decisions in go-mink.
{: .fs-6 .fw-300 }

## Table of contents
{: .no_toc .text-delta }

1. TOC
{:toc}

---

## What are ADRs?

Architecture Decision Records (ADRs) capture important architectural decisions made during the development of go-mink. Each ADR describes:

- **Context**: The situation and forces at play
- **Decision**: What we decided to do
- **Consequences**: The results of the decision (both positive and negative)

## ADR Index

| ID | Title | Status | Date |
|----|-------|--------|------|
| [ADR-001](001-event-sourcing-core) | Event Sourcing as Core Pattern | Accepted | 2024-01-15 |
| [ADR-002](002-postgresql-primary-storage) | PostgreSQL as Primary Storage | Accepted | 2024-01-15 |
| [ADR-003](003-cqrs-pattern) | CQRS Pattern Implementation | Accepted | 2024-02-01 |
| [ADR-004](004-command-bus-middleware) | Command Bus with Middleware Pipeline | Accepted | 2024-02-15 |
| [ADR-005](005-projection-types) | Three Projection Types | Accepted | 2024-03-01 |
| [ADR-006](006-optimistic-concurrency) | Optimistic Concurrency Control | Accepted | 2024-01-15 |
| [ADR-007](007-json-serialization) | JSON as Default Serialization | Accepted | 2024-01-15 |
| [ADR-008](008-testing-strategy) | BDD Testing Strategy | Accepted | 2024-04-01 |
| [ADR-009](009-error-handling) | Sentinel Errors with Typed Wrappers | Accepted | 2024-01-20 |
| [ADR-010](010-multi-tenancy) | Multi-tenancy via Metadata | Accepted | 2024-02-20 |

## ADR Template

When adding new ADRs, use this template:

```markdown
---
layout: default
title: "ADR-XXX: Title"
parent: Architecture Decision Records
nav_order: XXX
---

# ADR-XXX: Title

| Status | Date | Deciders |
|--------|------|----------|
| Proposed/Accepted/Deprecated/Superseded | YYYY-MM-DD | Team |

## Context

What is the issue that we're seeing that is motivating this decision?

## Decision

What is the change that we're proposing and/or doing?

## Consequences

### Positive
- Benefit 1
- Benefit 2

### Negative
- Drawback 1
- Drawback 2

### Neutral
- Side effect 1

## Alternatives Considered

### Alternative 1
Description and why it was rejected.

## References
- Link 1
- Link 2
```

## Status Definitions

| Status | Meaning |
|--------|---------|
| **Proposed** | Under discussion, not yet decided |
| **Accepted** | Decision has been made and implemented |
| **Deprecated** | No longer recommended, but still supported |
| **Superseded** | Replaced by another ADR |

## Contributing

When making significant architectural changes:

1. Create a new ADR with the next available number
2. Use the template above
3. Submit a PR with the ADR for review
4. Update the index table after acceptance
