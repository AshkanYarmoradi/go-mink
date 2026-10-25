export type CodeExample = {
  id: string;
  title: string;
  filename: string;
  description: string;
  code: string;
  href: string;
};

// These excerpts are checked against compiled Go functions in website/examples.
export const examples: CodeExample[] = [
  {
    id: "append",
    title: "Append events",
    filename: "append.go",
    description: "Plain Go structs, typed replay, and an explicit version check. Function excerpt; the complete example includes event types and store setup.",
    href: "https://github.com/AshkanYarmoradi/go-mink/tree/main/website/examples",
    code: `func appendOrder(ctx context.Context, store *mink.EventStore) error {
    // Register concrete types for typed replay.
    store.RegisterEvents(OrderCreated{}, ItemAdded{})

    events := []any{
        OrderCreated{CustomerID: "customer-456"},
        ItemAdded{SKU: "GO-01", Quantity: 2},
    }

    // NoStream rejects a second creation of this stream.
    return store.Append(ctx, "order-123", events,
        mink.ExpectVersion(mink.NoStream),
    )
}`,
  },
  {
    id: "replay",
    title: "Rebuild state",
    filename: "replay.go",
    description: "Load the stream and fold its events into a read model. This function follows the append example; projection execution stays explicit.",
    href: "https://github.com/AshkanYarmoradi/go-mink/tree/main/website/examples",
    code: `func replayOrder(ctx context.Context, store *mink.EventStore) (OrderSummary, error) {
    events, err := store.Load(ctx, "order-123")
    if err != nil {
        return OrderSummary{}, err
    }

    var summary OrderSummary
    for _, event := range events {
        switch data := event.Data.(type) {
        case OrderCreated:
            summary.CustomerID = data.CustomerID
        case ItemAdded:
            summary.Items += data.Quantity
        default:
            return OrderSummary{}, fmt.Errorf("unexpected event: %T", data)
        }
    }
    return summary, nil
}`,
  },
  {
    id: "postgres",
    title: "Use PostgreSQL",
    filename: "postgres.go",
    description: "Initialize durable storage explicitly, using the default JSON serializer. The caller closes the returned store. See the full example for imports and configuration.",
    href: "https://github.com/AshkanYarmoradi/go-mink/tree/main/website/examples",
    code: `func openPostgres(ctx context.Context, dsn string) (*mink.EventStore, error) {
    adapter, err := postgres.NewAdapter(dsn)
    if err != nil {
        return nil, err
    }

    store := mink.New(adapter)
    if err := store.Initialize(ctx); err != nil {
        return nil, errors.Join(err, store.Close())
    }
    store.RegisterEvents(OrderCreated{}, ItemAdded{})

    // The caller owns the store and closes it on shutdown.
    return store, nil
}`,
  },
];

export default examples;
