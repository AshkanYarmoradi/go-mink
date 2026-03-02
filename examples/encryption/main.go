// Package main demonstrates field-level encryption in go-mink.
//
// This example shows:
// - Encrypting PII fields (email, phone) at rest
// - Decrypting transparently on load
// - Per-tenant encryption keys
// - Crypto-shredding (key revocation makes data unrecoverable)
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/AshkanYarmoradi/go-mink/encryption"
	"github.com/AshkanYarmoradi/go-mink/encryption/local"
)

// Domain Events

type CustomerCreated struct {
	CustomerID string `json:"customer_id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	Country    string `json:"country"`
}

// Aggregate

type Customer struct {
	mink.AggregateBase
	Name    string
	Email   string
	Phone   string
	Country string
}

func NewCustomer(id string) *Customer {
	return &Customer{
		AggregateBase: mink.NewAggregateBase(id, "Customer"),
	}
}

func (c *Customer) Create(name, email, phone, country string) {
	c.Apply(CustomerCreated{
		CustomerID: c.AggregateID(),
		Name:       name,
		Email:      email,
		Phone:      phone,
		Country:    country,
	})
}

func (c *Customer) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case CustomerCreated:
		c.Name = e.Name
		c.Email = e.Email
		c.Phone = e.Phone
		c.Country = e.Country
	default:
		return fmt.Errorf("unknown event: %T", event)
	}
	c.IncrementVersion()
	return nil
}

func main() {
	ctx := context.Background()

	// 1. Set up encryption keys (one per tenant)
	tenantAKey := generateKey()
	tenantBKey := generateKey()

	provider, err := local.New(
		local.WithKey("tenant-A", tenantAKey),
		local.WithKey("tenant-B", tenantBKey),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = provider.Close() }()

	// 2. Configure field-level encryption
	encConfig := mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID("tenant-A"),
		// Encrypt email and phone — name and country remain queryable
		mink.WithEncryptedFields("CustomerCreated", "email", "phone"),
		// Per-tenant key resolver
		mink.WithTenantKeyResolver(func(tenantID string) string {
			return "tenant-" + tenantID
		}),
		// Crypto-shredding handler: graceful degradation when key is revoked
		mink.WithDecryptionErrorHandler(func(err error, eventType string, metadata mink.Metadata) error {
			if errors.Is(err, encryption.ErrKeyRevoked) {
				fmt.Printf("  [SHREDDED] Key revoked for tenant %s — returning encrypted data\n", metadata.TenantID)
				return nil
			}
			return err
		}),
	)

	// 3. Create event store with encryption
	adapter := memory.NewAdapter()
	store := mink.New(adapter, mink.WithFieldEncryption(encConfig))
	store.RegisterEvents(CustomerCreated{})

	// ── Demo: Encrypt on save, decrypt on load ──

	fmt.Println("=== Field-Level Encryption Demo ===")
	fmt.Println()

	// Save a customer with tenant A key
	customer := NewCustomer("cust-1")
	customer.Create("Alice Smith", "alice@example.com", "+1-555-0100", "US")
	if err := store.SaveAggregate(ctx, customer); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Saved customer with encrypted PII fields")

	// Inspect raw data at rest
	raw, _ := store.LoadRaw(ctx, "Customer-cust-1", 0)
	fmt.Printf("Raw data at rest: %s\n", raw[0].Data)
	fmt.Printf("Encrypted fields: %v\n", mink.GetEncryptedFields(raw[0].Metadata))
	fmt.Printf("Key ID: %s\n", mink.GetEncryptionKeyID(raw[0].Metadata))
	fmt.Println()

	// Load — automatically decrypted
	loaded := NewCustomer("cust-1")
	if err := store.LoadAggregate(ctx, loaded); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Loaded customer: Name=%s, Email=%s, Phone=%s\n", loaded.Name, loaded.Email, loaded.Phone)
	fmt.Println()

	// ── Demo: Per-tenant keys ──

	fmt.Println("=== Per-Tenant Encryption ===")
	fmt.Println()

	// Save with tenant B key
	if err := store.Append(ctx, "Customer-cust-2", []interface{}{
		CustomerCreated{CustomerID: "cust-2", Name: "Bob Jones", Email: "bob@tenant-b.com", Phone: "+44-20-1234", Country: "UK"},
	}, mink.WithAppendMetadata(mink.Metadata{TenantID: "B"})); err != nil {
		log.Fatal(err)
	}

	rawB, _ := store.LoadRaw(ctx, "Customer-cust-2", 0)
	fmt.Printf("Tenant B key ID: %s\n", mink.GetEncryptionKeyID(rawB[0].Metadata))

	eventsB, _ := store.Load(ctx, "Customer-cust-2")
	fmt.Printf("Tenant B email (decrypted): %s\n", eventsB[0].Data.(CustomerCreated).Email)
	fmt.Println()

	// ── Demo: Crypto-shredding ──

	fmt.Println("=== Crypto-Shredding (GDPR Right to Erasure) ===")
	fmt.Println()

	// Revoke tenant B's key — simulates GDPR deletion request
	fmt.Println("Revoking tenant B encryption key...")
	if err := provider.RevokeKey("tenant-B"); err != nil {
		log.Fatal(err)
	}

	// Try to load tenant B data — handler returns nil, data stays encrypted
	eventsB2, err := store.Load(ctx, "Customer-cust-2")
	if err != nil {
		log.Fatal(err)
	}
	e := eventsB2[0].Data.(CustomerCreated)
	fmt.Printf("Tenant B after shredding: Name=%s, Email=%s (encrypted)\n", e.Name, e.Email)
	fmt.Println()

	// Tenant A data is still accessible
	eventsA, _ := store.Load(ctx, "Customer-cust-1")
	eA := eventsA[0].Data.(CustomerCreated)
	fmt.Printf("Tenant A still accessible: Name=%s, Email=%s\n", eA.Name, eA.Email)

	fmt.Println()
	fmt.Println("Done!")
}

func generateKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		log.Fatal(err)
	}
	return key
}
