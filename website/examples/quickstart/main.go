// Package main demonstrates the code examples on the go-mink homepage.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"go-mink.dev"
	"go-mink.dev/adapters/memory"
)

func main() {
	usePostgres := flag.Bool("postgres", false, "use PostgreSQL via DATABASE_URL instead of memory")
	flag.Parse()
	if err := run(*usePostgres); err != nil {
		log.Fatal(err)
	}
}

func run(usePostgres bool) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var store *mink.EventStore
	if usePostgres {
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return errors.New("set DATABASE_URL to a disposable PostgreSQL database")
		}
		store, err = openPostgres(ctx, dsn)
		if err != nil {
			return fmt.Errorf("initialize PostgreSQL: %w", err)
		}
	} else {
		// Memory is convenient for this demo; it is not durable storage.
		store = mink.New(memory.NewAdapter())
	}
	defer func() { err = errors.Join(err, store.Close()) }()

	if err := appendOrder(ctx, store); err != nil {
		return fmt.Errorf("append order: %w", err)
	}
	summary, err := replayOrder(ctx, store)
	if err != nil {
		return fmt.Errorf("replay order: %w", err)
	}
	fmt.Printf("Order for %s has %d items\n", summary.CustomerID, summary.Items)
	return nil
}
