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

### Requirement: Nullable scalar columns read NULL as the field's zero value
The read-model store SHALL read a `NULL` from a column declared `nullable` whose
destination struct field is a **non-pointer scalar** kind — `string`, the signed and
unsigned integer kinds, `float32`/`float64`, `bool`, or `time.Time` — as that field's Go
zero value, rather than failing the scan. It SHALL do so by scanning such columns through
an intermediate `sql.Null[T]` holder and coalescing (`NULL` → zero value; otherwise the
scanned value). `Find`, `FindOne`, and `Get` SHALL apply this, so a `NULL` in any row of
a result set never aborts the read. Pointer fields and byte/JSON columns (`[]byte`,
slices, maps) SHALL keep scanning `NULL` to `nil` unchanged. The write path SHALL be
unaffected: a zero-value scalar field persists as its zero value, never `NULL` — a
distinguishable `NULL` is expressed with a pointer field.

#### Scenario: Nullable string column holding NULL is read as ""
- **WHEN** a row's `nullable` `TEXT` column is `NULL` and its field is a `string`, and `Find`/`FindOne`/`Get` reads it
- **THEN** the field is `""` and the call returns no error (previously it failed with `converting NULL to string is unsupported`)

#### Scenario: A NULL cell does not abort the whole result set
- **WHEN** a `Find` result contains a mix of rows where a `nullable` scalar column is `NULL` in some and populated in others
- **THEN** every row scans — the populated rows keep their values and the `NULL` rows get the zero value — and no row's `NULL` fails the batch

#### Scenario: Nullable numeric / bool / time columns coalesce to zero
- **WHEN** a `nullable` column is `NULL` and its field is an integer, unsigned, float, `bool`, or `time.Time`
- **THEN** the field is `0` / `false` / the zero `time.Time` respectively, with no error

#### Scenario: Non-NULL values and pointer/JSON fields are unchanged
- **WHEN** a `nullable` scalar column holds a real value, or a field is a pointer (`*string`, …) or a `[]byte`/JSON column that is `NULL`
- **THEN** the scalar reads its stored value exactly as before, and the pointer/JSON field reads `NULL` as `nil` exactly as before

#### Scenario: Writing a zero value does not write NULL
- **WHEN** a read model persists a scalar field whose value is the Go zero value (`""`, `0`, `false`, zero time)
- **THEN** the column stores that zero value, not `NULL` (persistence semantics are unchanged; the read-side coalescing does not leak into writes)

#### Scenario: A non-NULL value that overflows the destination field is a typed error
- **WHEN** a `nullable` numeric column holds a non-`NULL` value that does not fit its destination field — a value beyond the field's range (e.g. `int8`) or a negative value read into an unsigned field, written out of band
- **THEN** the read fails with a typed `*ColumnValueRangeError` (matching `errors.Is(err, ErrColumnValueRange)`) naming the column and field, rather than silently truncating or wrapping the value — preserving the fail-loud behavior of a direct `database/sql` scan (which coalescing through a wide `int64`/`float64` intermediate would otherwise lose)

### Requirement: A NULL in a non-nullable column yields a typed, actionable error
The read-model store SHALL, when a column **not** declared `nullable` nonetheless
contains `NULL` (an external write or a manual migration), fail with a typed
`*NullColumnError` that names the offending column and struct field and wraps the
underlying driver error (retrievable via `errors.Unwrap`), rather than surfacing the
driver's opaque `converting NULL to <type>` message. It MUST NOT silently substitute a
zero value for a non-nullable column.

#### Scenario: Unexpected NULL in a non-nullable column is named
- **WHEN** a scalar column that is not tagged `nullable` is read and its stored value is `NULL`
- **THEN** the read returns a `*NullColumnError` whose message names the column and field and suggests the `nullable` tag, and `errors.Unwrap` yields the original driver error

