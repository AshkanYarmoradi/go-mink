# Design — nullable-column-read-safety

## Where the gap is

`adapters/postgres/readmodel.go`:

- `parseFieldTag` already records `ColumnType.Nullable` per column; the parsed columns
  live on the repository as `r.tableSchema.Columns`, ordered the same as `r.columns`.
- `getScanPointers(model *T) []interface{}` builds one scan target per column as
  `val.Field(i).Addr().Interface()` — i.e. `rows.Scan` writes straight into the struct
  field. `scanRows`, `FindOne`, and `Get` all funnel through it.
- `database/sql` cannot assign a SQL `NULL` into a `*string`/`*int64`/`*bool`/`*float64`/
  `*time.Time`; it returns `converting NULL to <type> is unsupported`, which fails the
  entire `Scan` call.

Pointer fields (`*string`, …) and byte/JSON columns (`[]byte`→BYTEA, other slices/maps→
JSONB) already scan `NULL` cleanly (to `nil`), so they are out of scope.

## Approach — coalesce nullable scalars through `sql.Null[T]`

Go 1.25 provides the generic `database/sql.Null[T]` (V + Valid), whose `Scan` delegates
to the driver's `convertAssign` and reports `Valid == false` on `NULL`. Use it as an
intermediate holder for exactly the fields that need it, then copy the value (or the zero
value) back into the struct field.

### 1. Precompute the wrap set once (construction, not per row)

At repository construction (where `r.columns` / `r.tableSchema` are finalized), compute
the set of **field indices** that both (a) map to a `Nullable` column and (b) have a
non-pointer scalar `reflect.Kind`:

```
scalar kinds: String | Bool |
              Int, Int8..Int64 | Uint, Uint8..Uint64 |
              Float32, Float64 |
              Struct where type == time.Time
```

Everything else (pointer, `[]byte`, other slice/map/struct → JSONB) keeps the current
direct-address scan. Store this as e.g. `nullableScalarFields map[int]struct{}` (or a
precomputed per-column flag parallel to `r.columns`). This keeps the hot path free of new
reflection: the decision is a map/slice lookup.

### 2. Scan target + finalizer per row

Change the scan helper so a wrapped field contributes a holder to the `ptrs` slice **and**
a finalizer closure that copies the holder into the field after `Scan`:

```go
// per row
ptrs := make([]interface{}, len(r.columns))
var finalize []func()

for each mapped field i -> column index idx:
    if i in nullableScalarFields:
        switch f := val.Field(i); f.Kind() {
        case reflect.String:
            h := new(sql.Null[string]);  ptrs[idx] = h
            finalize = append(finalize, func(){ f.SetString(h.V) })   // V is "" when NULL
        case reflect.Bool:
            h := new(sql.Null[bool]);    ptrs[idx] = h
            finalize = append(finalize, func(){ f.SetBool(h.V) })
        case Int kinds:
            h := new(sql.Null[int64]);   ptrs[idx] = h
            finalize = append(finalize, func(){ f.SetInt(h.V) })
        case Uint kinds:
            h := new(sql.Null[int64]);   ptrs[idx] = h                // driver returns int64
            finalize = append(finalize, func(){ f.SetUint(uint64(h.V)) })
        case Float kinds:
            h := new(sql.Null[float64]); ptrs[idx] = h
            finalize = append(finalize, func(){ f.SetFloat(h.V) })
        case time.Time:
            h := new(sql.Null[time.Time]); ptrs[idx] = h
            finalize = append(finalize, func(){ f.Set(reflect.ValueOf(h.V)) })
        }
    else:
        ptrs[idx] = val.Field(i).Addr().Interface()   // unchanged

// after rows.Scan(ptrs...):
for _, fn := range finalize { fn() }
```

Because `sql.Null[T]`'s zero `V` **is** the Go zero value, a `NULL` naturally coalesces to
`""`/`0`/`false`/zero-time — no special-casing of `Valid` needed for the Required
behavior. (`Valid` is only needed if a consumer wants to distinguish "NULL" from "zero",
which pointer fields already express; out of scope here.)

Shape this as a small internal type (e.g. `rowScan{ptrs, finalize}`) returned by the
existing `getScanPointers`, so `scanRows`/`FindOne`/`Get` gain one `for _, fn := range …`
line and nothing else moves.

### 3. Write path unchanged

`extractValues` keeps calling `val.Field(i).Interface()`, so a zero-value scalar is
written as that zero value, not `NULL`. This preserves persistence semantics: `nullable`
governs *reads* here; to *write* `NULL`, use a pointer field (already supported).

## Good-to-have — typed error for NULL in a non-nullable column

Fields **not** in the wrap set still scan directly, so a `NULL` in a non-nullable scalar
column still fails — but with the driver's opaque message. Wrap that: when `rows.Scan`
returns an error matching `converting NULL to`, map it to a typed
`*NullColumnError{Column, Field, GoType}` implementing `error` + `Unwrap`, with a message
that names the column/field and suggests adding `nullable`. This turns an unactionable
failure into a fix hint, without hiding it. (The Required work already removes the common,
tag-declared case; this only improves the diagnostic for the remaining misconfiguration.)

## Invariants preserved

- **Append-only** — no event-store or history change; this is purely the read-model scan.
- **Zero overhead when unused** — only `nullable` scalar columns are wrapped, decided once
  at construction; non-nullable, pointer, and JSON/byte columns take the identical path as
  today. A read model with no nullable scalar columns allocates no finalizers.
- **No forced schema change** — DDL is untouched; the column is already nullable.
- **Backward compatible** — non-nullable columns behave identically; a nullable column that
  never holds `NULL` scans the same value as before; a nullable column that *does* hold
  `NULL` now returns the zero value instead of failing the whole query. No public API or
  tag syntax changes; existing pointer-field workarounds keep working.

## Testing

Table-driven, against both the Postgres adapter (real `NULL` round-trip) and the existing
read-model test harness:

- For each scalar kind: a row with the column `NULL` → `Find`/`FindOne`/`Get` succeed and
  the field is the Go zero value; a row with a real value → unchanged.
- A result set mixing NULL and non-NULL rows scans fully (no partial/aborted scan).
- A non-nullable scalar column set to `NULL` out of band → (Good-to-have) returns
  `*NullColumnError` naming the column/field; (baseline) still errors, never silently
  zeroes.
- Pointer and `[]byte`/JSONB nullable fields: unchanged (NULL → `nil`).
- Write path: persisting a zero-value scalar still stores the zero value, not `NULL`.
