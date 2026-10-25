# Homepage examples

The homepage displays function excerpts from `quickstart/`. The complete example includes imports, event definitions, store setup, cleanup, and error handling.

From the repository root, with Go 1.26 or newer:

```sh
go run ./website/examples/quickstart
go test ./website/examples/quickstart
```

Expected output:

```text
Order for customer-456 has 2 items
```

The default uses the in-memory adapter. No database is required, and the events disappear when the process exits. The replay example manually folds typed events into an `OrderSummary`; the event store does not run projections automatically.

To try PostgreSQL, set `DATABASE_URL` to a **disposable database**, then run:

```sh
go run ./website/examples/quickstart -postgres
```

This explicitly initializes the adapter's schema and writes the fixed `order-123` stream. A second run against the same database returns a concurrency conflict by design: `mink.NoStream` requires that the stream does not exist. Use a fresh database to repeat the demonstration. The example does not delete existing data.

The `snippet:*` markers identify the exact functions shown on the homepage. Run `node website/examples/check-snippets.mjs` to ensure the displayed excerpts still match the compiled Go source.
