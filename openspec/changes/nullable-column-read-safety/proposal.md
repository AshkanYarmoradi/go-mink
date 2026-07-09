## Why

The `mink:"...,nullable"` tag on a read-model field means two different things today,
and they disagree:

- **On the write / DDL side** it works: `parseFieldTag` sets `ColumnType.Nullable`, so
  `Migrate` emits the column *without* `NOT NULL`, and the column can legitimately hold
  `NULL` (a projection or an out-of-band `UPDATE` may set it so).
- **On the read side it does not.** `getScanPointers` hands `rows.Scan` the field's own
  address for every column — `val.Field(i).Addr().Interface()`. For a **non-pointer
  scalar** field (`string`, the integer/unsigned kinds, `float`, `bool`, `time.Time`) a
  stored `NULL` makes `database/sql` fail with `converting NULL to <type> is
  unsupported`. That error aborts the whole `rows.Scan`, so `Find`, `FindOne`, and `Get`
  fail for the *entire* result set — one `NULL` cell takes down the query.

So a column the tag explicitly declares nullable cannot actually be read back once it
holds a `NULL`. A caller only discovers this after a `NULL` reaches the column (e.g. a
read model that starts writing `NULL`, a redaction/erasure step that blanks a column to
`NULL`, or a manual data fix), at which point reads of that read model break entirely.
Callers work around it by declaring the field a pointer (`*string`) or by scrupulously
never writing `NULL` — but the tag already promised nullability, so `nullable` should
simply be safe to read.

This is a read-path correctness gap in the `read-model-store` capability, independent of
any consumer. Fixing it in the store makes the tag honest for every adapter user.

## What Changes

### Required

- **NULL-safe reads for nullable scalar columns** (`read-model-store`). When a column is
  `Nullable` and its destination field is a non-pointer scalar kind (`string`, signed and
  unsigned integers, `float32/64`, `bool`, `time.Time`), the Postgres repository SHALL
  scan it through an intermediate `sql.Null[T]` holder and **coalesce**: a `NULL` yields
  the field's zero value; a non-`NULL` yields the scanned value. This applies to every
  row-reading path — `Find`, `FindOne`, `Get`. Pointer fields and `[]byte`/JSONB fields
  are already `NULL`-safe (they scan `NULL` to `nil`) and keep scanning directly,
  unchanged.

### Good-to-have

- **Actionable error for a NULL in a *non-nullable* column** (`read-model-store`). The
  case the Required work deliberately does not cover — a column NOT tagged `nullable`
  that nonetheless contains `NULL` (external write, manual migration) — SHALL surface a
  typed `*NullColumnError` naming the column and field and suggesting the `nullable` tag,
  instead of the driver's opaque `converting NULL to <type>`.

## Non-Goals

- **No change to the write path.** `extractValues`/upsert keep writing a Go zero value
  as that zero value (`''`, `0`, `false`, zero time), never `NULL`. Persisting a real
  `NULL` remains the job of a pointer field. This change is read-only.
- **No nullability inference from the Go type.** The explicit `nullable` tag stays the
  trigger for scalar coalescing; pointer fields remain independently `NULL`-safe exactly
  as today. A non-pointer scalar field left un-tagged is treated as before.
- **No in-memory adapter change.** The memory adapter stores Go values directly and has
  no SQL `NULL`-scan step, so it is unaffected and needs no coalescing.
- **No new DB-schema or migration behavior.** DDL is unchanged; the column is already
  nullable. No event-store or history change (append-only preserved).
