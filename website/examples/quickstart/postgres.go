package main

import (
	"context"
	"errors"

	"go-mink.dev"
	"go-mink.dev/adapters/postgres"
)

// snippet:postgres:start
func openPostgres(ctx context.Context, dsn string) (*mink.EventStore, error) {
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
}

// snippet:postgres:end
