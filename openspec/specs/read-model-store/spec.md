# read-model-store Specification

## Purpose
TBD - created by archiving change fix-review-correctness-bugs. Update Purpose after archive.
## Requirements
### Requirement: Query filters never degrade to an unqualified statement
The read-model query builder SHALL treat a caller-supplied, non-empty filter set that
resolves to zero column conditions as an error, returning `ErrUnknownFilterField` (a
`*UnknownFilterFieldError` naming the unresolved field) rather than an empty `WHERE`
clause. `Find`, `Count`, `DeleteMany`, and `UpdateMany` SHALL propagate this error, so a
query built from unknown or misspelled fields can never scan or delete the whole table.
An intentional match-all SHALL remain expressible only via an explicitly empty filter
set.

#### Scenario: Misspelled sole filter field is rejected
- **WHEN** `DeleteMany` is called with a query whose only filter names a field that maps to no column
- **THEN** it returns `ErrUnknownFilterField` naming that field and executes no `DELETE`

#### Scenario: Explicit match-all still allowed
- **WHEN** a query is built with no filters at all
- **THEN** `Find`/`Count`/`DeleteMany` operate over all rows as before (no error)

#### Scenario: Mixed known and unknown fields
- **WHEN** a query has one resolvable filter and one unknown field
- **THEN** the builder returns `ErrUnknownFilterField` for the unknown field rather than silently applying only the resolvable one

