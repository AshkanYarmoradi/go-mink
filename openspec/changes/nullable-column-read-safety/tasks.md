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
