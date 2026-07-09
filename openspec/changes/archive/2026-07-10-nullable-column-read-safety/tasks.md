# Tasks — nullable-column-read-safety

Branch from `develop`; PR targets `develop`. `gofmt` + `golangci-lint` clean. Group
Required before Good-to-have; ordered by dependency.

## Required — NULL-safe reads for nullable scalar columns

- [x] 1.1 Precompute the wrap set at repository construction: the field indices whose
  column is `Nullable` (from `r.tableSchema.Columns`) AND whose `reflect.Kind` is a
  non-pointer scalar (`String`, `Bool`, the `Int*`/`Uint*` kinds, `Float32/64`, or a
  `time.Time` struct). Store parallel to `r.columns` so the scan path does a lookup, not
  per-row type analysis.
- [x] 1.2 Refactor the scan-target builder (`getScanPointers`) to return the scan `ptrs`
  plus a finalizer slice: for a wrapped field, put a kind-matched `sql.Null[T]` holder in
  `ptrs` and append a copy-back closure; for every other field keep
  `val.Field(i).Addr().Interface()` unchanged.
- [x] 1.3 Run the finalizers after `rows.Scan` in every reader (`scanRows`, `FindOne`,
  `Get`) so `NULL` coalesces to the field's zero value and a valid value is assigned via
  the matching `reflect.Set*` (note `Uint*` reads through `sql.Null[int64]`).
- [x] 1.4 Confirm the write path (`extractValues`/upsert) is untouched — a zero-value
  scalar still persists as the zero value, never `NULL`.
- [x] 1.5 Table-driven tests (Postgres adapter + read-model harness): per scalar kind,
  `NULL` cell → `Find`/`FindOne`/`Get` succeed with the Go zero value; real value →
  unchanged; a mixed NULL/non-NULL result set scans fully; pointer and `[]byte`/JSONB
  nullable fields still scan `NULL` → `nil`; a persisted zero value stays non-`NULL`.
- [x] 1.6 Document the read-side guarantee of `nullable` on the exported repository API /
  the `mink:"...,nullable"` tag doc: reading a `NULL` from a nullable scalar column yields
  the field's zero value; persist a distinguishable `NULL` with a pointer field.

## Good-to-have — actionable error for NULL in a non-nullable column

- [x] 2.1 Add a typed `*NullColumnError{Column, Field, GoType}` (with `Error`/`Unwrap`)
  and map the driver's `converting NULL to <type>` failure to it when a **non-nullable**
  scalar column yields `NULL`, with a message that names the column/field and suggests the
  `nullable` tag. Table-driven test for the mapping; keep the underlying error via
  `Unwrap`.

## Review hardening (post-PR-review #183)

- [x] 3.1 Range-check nullable integer/float coalescing: since values are read through a
  wide `int64`/`float64` intermediate, an out-of-range non-`NULL` value (beyond the field's
  range, or negative into an unsigned field) now fails with a typed `*ColumnValueRangeError`
  (sentinel `ErrColumnValueRange`) instead of silently truncating/wrapping — restoring the
  fail-loud parity of a direct `database/sql` scan. Finalizers return an error; unit +
  integration tests for each kind.
- [x] 3.2 Add sentinel `ErrNullColumn` + `Is()` on `*NullColumnError`, matching the
  package's sentinel-plus-typed-error convention (`errors.Is(err, ErrNullColumn)`).
- [x] 3.3 Surface the typed `*NullColumnError` consistently from `Find` and `Get` (Get no
  longer wraps the typed error in `get failed:`, matching the `scanRows` path).
- [x] 3.4 Precompute the field→column mapping (`columnField`) at construction alongside the
  scan kinds, removing per-row `newFieldMapper`/`getColumnName` reflection from the scan path.
- [x] 3.5 Add a `TxRepository` nullable-read test asserting NULL coalescing through a
  transaction.
