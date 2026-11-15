---
title: "ADR-012: Portable Case-Insensitive Text Search"
sidebar_position: 12
---

# ADR-012: Portable Case-Insensitive Text Search

| Status | Date |
|--------|------|
| Proposed | 2026-11-15 |

## Context

Read-model queries need to find a literal substring without requiring the caller
to reproduce the stored casing. Existing `Like` and `Contains` filters have
established behavior, and the public query builder is shared across adapters.

## Decision

Add `FilterOpContainsIgnoreCase` to the shared query vocabulary. It accepts a
string operand, treats wildcard characters literally, and excludes null text
fields. The adapter translates the filter. PostgreSQL uses a bound, escaped
`ILIKE` pattern. The in-memory repository compares lowercased text.

## Consequences

Callers can opt in against existing text fields with no schema migration.
Existing queries and public signatures retain their behavior. A future adapter
must implement the operator or return an unsupported-query error.

Case mapping follows each adapter and its locale. Applications that need
identical matching across locales can store normalized text and normalize
queries with the same function.

## Alternatives Considered

- **Expose `ILIKE` in the query API.** This would couple callers to a PostgreSQL pattern operator and leave wildcard escaping to the caller.
- **Change `FilterOpContains`.** This would change existing text queries and conflate substring matching with JSON containment.
- **Require a normalized column.** This supports deterministic case mapping but requires a backfill and maintenance on writes. It remains an application option.

## References

- [Read-model query guide](../core/read-models.md#case-insensitive-matching)
