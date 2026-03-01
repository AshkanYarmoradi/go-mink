package mink_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"

	"github.com/AshkanYarmoradi/go-mink"
	"github.com/AshkanYarmoradi/go-mink/adapters/memory"
	"github.com/AshkanYarmoradi/go-mink/encryption"
	"github.com/AshkanYarmoradi/go-mink/encryption/local"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// E2E Test: Field-Level Encryption
// =============================================================================

// --- Test Domain Events ---

type UserRegistered struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Phone  string `json:"phone"`
}

type OrderPlaced struct {
	OrderID string  `json:"order_id"`
	UserID  string  `json:"user_id"`
	Amount  float64 `json:"amount"`
}

// --- Test Aggregate ---

type userProfile struct {
	mink.AggregateBase
	Name  string
	Email string
	Phone string
}

func newUserProfile(id string) *userProfile {
	return &userProfile{
		AggregateBase: mink.NewAggregateBase(id, "User"),
	}
}

func (u *userProfile) Register(name, email, phone string) {
	u.Apply(UserRegistered{
		UserID: u.AggregateID(),
		Name:   name,
		Email:  email,
		Phone:  phone,
	})
}

func (u *userProfile) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	case UserRegistered:
		u.Name = e.Name
		u.Email = e.Email
		u.Phone = e.Phone
	default:
		return fmt.Errorf("unknown event type: %T", event)
	}
	u.IncrementVersion()
	return nil
}

// --- Test Helpers ---

func testEncryptionKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	return key
}

func newEncryptedStore(t *testing.T, provider encryption.Provider, keyID string) (*mink.EventStore, *memory.MemoryAdapter) {
	t.Helper()
	adapter := memory.NewAdapter()

	config := mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID(keyID),
		mink.WithEncryptedFields("UserRegistered", "email", "phone"),
	)

	store := mink.New(adapter, mink.WithFieldEncryption(config))
	store.RegisterEvents(UserRegistered{}, OrderPlaced{})
	return store, adapter
}

// =============================================================================
// Test: Full round-trip — encrypt on append, decrypt on load
// =============================================================================

func TestE2E_Encryption_FullRoundTrip(t *testing.T) {
	ctx := context.Background()
	key := testEncryptionKey(t)
	provider := local.New(local.WithKey("master-1", key))
	defer provider.Close()

	store, _ := newEncryptedStore(t, provider, "master-1")

	// Append event with PII fields
	err := store.Append(ctx, "User-user-1", []interface{}{
		UserRegistered{
			UserID: "user-1",
			Name:   "John Doe",
			Email:  "john@example.com",
			Phone:  "+1234567890",
		},
	})
	require.NoError(t, err)

	// Load — should be automatically decrypted
	events, err := store.Load(ctx, "User-user-1")
	require.NoError(t, err)
	require.Len(t, events, 1)

	e, ok := events[0].Data.(UserRegistered)
	require.True(t, ok, "expected UserRegistered, got %T", events[0].Data)
	assert.Equal(t, "user-1", e.UserID)
	assert.Equal(t, "John Doe", e.Name)
	assert.Equal(t, "john@example.com", e.Email)
	assert.Equal(t, "+1234567890", e.Phone)
}

// =============================================================================
// Test: Raw data is actually encrypted at rest
// =============================================================================

func TestE2E_Encryption_DataAtRest(t *testing.T) {
	ctx := context.Background()
	key := testEncryptionKey(t)
	provider := local.New(local.WithKey("master-1", key))
	defer provider.Close()

	store, _ := newEncryptedStore(t, provider, "master-1")

	err := store.Append(ctx, "User-user-2", []interface{}{
		UserRegistered{
			UserID: "user-2",
			Name:   "Jane Doe",
			Email:  "jane@secret.com",
			Phone:  "+9876543210",
		},
	})
	require.NoError(t, err)

	// Load raw (no decryption) to verify data is encrypted at rest
	raw, err := store.LoadRaw(ctx, "User-user-2", 0)
	require.NoError(t, err)
	require.Len(t, raw, 1)

	// Name should NOT be encrypted
	assert.Contains(t, string(raw[0].Data), "Jane Doe")

	// Email and phone SHOULD be encrypted (not present in plaintext)
	assert.NotContains(t, string(raw[0].Data), "jane@secret.com")
	assert.NotContains(t, string(raw[0].Data), "+9876543210")

	// Encryption metadata should be present
	assert.True(t, mink.IsEncrypted(raw[0].Metadata))
	assert.Equal(t, "master-1", mink.GetEncryptionKeyID(raw[0].Metadata))
	fields := mink.GetEncryptedFields(raw[0].Metadata)
	assert.ElementsMatch(t, []string{"email", "phone"}, fields)
}

// =============================================================================
// Test: Non-encrypted events are unaffected
// =============================================================================

func TestE2E_Encryption_NonEncryptedEvents(t *testing.T) {
	ctx := context.Background()
	key := testEncryptionKey(t)
	provider := local.New(local.WithKey("master-1", key))
	defer provider.Close()

	store, _ := newEncryptedStore(t, provider, "master-1")

	// OrderPlaced has no encrypted fields configured
	err := store.Append(ctx, "Order-order-1", []interface{}{
		OrderPlaced{OrderID: "order-1", UserID: "user-1", Amount: 99.99},
	})
	require.NoError(t, err)

	raw, err := store.LoadRaw(ctx, "Order-order-1", 0)
	require.NoError(t, err)
	require.Len(t, raw, 1)

	// No encryption metadata
	assert.False(t, mink.IsEncrypted(raw[0].Metadata))

	// Data is in plaintext
	assert.Contains(t, string(raw[0].Data), "order-1")
	assert.Contains(t, string(raw[0].Data), "99.99")
}

// =============================================================================
// Test: Zero overhead when encryption not configured
// =============================================================================

func TestE2E_Encryption_ZeroOverhead(t *testing.T) {
	ctx := context.Background()
	adapter := memory.NewAdapter()

	// No encryption configured
	store := mink.New(adapter)
	store.RegisterEvents(UserRegistered{})

	err := store.Append(ctx, "User-user-3", []interface{}{
		UserRegistered{UserID: "user-3", Name: "Bob", Email: "bob@test.com", Phone: "+111"},
	})
	require.NoError(t, err)

	events, err := store.Load(ctx, "User-user-3")
	require.NoError(t, err)
	require.Len(t, events, 1)

	e := events[0].Data.(UserRegistered)
	assert.Equal(t, "bob@test.com", e.Email)

	// No encryption metadata
	raw, err := store.LoadRaw(ctx, "User-user-3", 0)
	require.NoError(t, err)
	assert.False(t, mink.IsEncrypted(raw[0].Metadata))
}

// =============================================================================
// Test: SaveAggregate and LoadAggregate with encryption
// =============================================================================

func TestE2E_Encryption_AggregateRoundTrip(t *testing.T) {
	ctx := context.Background()
	key := testEncryptionKey(t)
	provider := local.New(local.WithKey("master-1", key))
	defer provider.Close()

	store, _ := newEncryptedStore(t, provider, "master-1")

	// Save aggregate
	user := newUserProfile("user-4")
	user.Register("Alice", "alice@example.com", "+555")

	err := store.SaveAggregate(ctx, user)
	require.NoError(t, err)

	// Load aggregate — should decrypt automatically
	loaded := newUserProfile("user-4")
	err = store.LoadAggregate(ctx, loaded)
	require.NoError(t, err)

	assert.Equal(t, "Alice", loaded.Name)
	assert.Equal(t, "alice@example.com", loaded.Email)
	assert.Equal(t, "+555", loaded.Phone)
}

// =============================================================================
// Test: Per-tenant encryption keys
// =============================================================================

func TestE2E_Encryption_PerTenantKeys(t *testing.T) {
	ctx := context.Background()

	keyA := testEncryptionKey(t)
	keyB := testEncryptionKey(t)
	provider := local.New(
		local.WithKey("tenant-A-key", keyA),
		local.WithKey("tenant-B-key", keyB),
	)
	defer provider.Close()

	adapter := memory.NewAdapter()
	config := mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID("tenant-A-key"),
		mink.WithEncryptedFields("UserRegistered", "email"),
		mink.WithTenantKeyResolver(func(tenantID string) string {
			return "tenant-" + tenantID + "-key"
		}),
	)
	store := mink.New(adapter, mink.WithFieldEncryption(config))
	store.RegisterEvents(UserRegistered{})

	// Append with tenant A metadata
	err := store.Append(ctx, "User-tenantA-1", []interface{}{
		UserRegistered{UserID: "t-a-1", Name: "TenantA User", Email: "a@tenant.com"},
	}, mink.WithAppendMetadata(mink.Metadata{TenantID: "A"}))
	require.NoError(t, err)

	// Append with tenant B metadata
	err = store.Append(ctx, "User-tenantB-1", []interface{}{
		UserRegistered{UserID: "t-b-1", Name: "TenantB User", Email: "b@tenant.com"},
	}, mink.WithAppendMetadata(mink.Metadata{TenantID: "B"}))
	require.NoError(t, err)

	// Verify different keys were used
	rawA, _ := store.LoadRaw(ctx, "User-tenantA-1", 0)
	rawB, _ := store.LoadRaw(ctx, "User-tenantB-1", 0)
	assert.Equal(t, "tenant-A-key", mink.GetEncryptionKeyID(rawA[0].Metadata))
	assert.Equal(t, "tenant-B-key", mink.GetEncryptionKeyID(rawB[0].Metadata))

	// Both should decrypt correctly
	eventsA, err := store.Load(ctx, "User-tenantA-1")
	require.NoError(t, err)
	assert.Equal(t, "a@tenant.com", eventsA[0].Data.(UserRegistered).Email)

	eventsB, err := store.Load(ctx, "User-tenantB-1")
	require.NoError(t, err)
	assert.Equal(t, "b@tenant.com", eventsB[0].Data.(UserRegistered).Email)
}

// =============================================================================
// Test: Crypto-shredding — revoke key, data is unrecoverable
// =============================================================================

func TestE2E_Encryption_CryptoShredding(t *testing.T) {
	ctx := context.Background()
	key := testEncryptionKey(t)
	provider := local.New(local.WithKey("master-1", key))
	defer provider.Close()

	adapter := memory.NewAdapter()
	config := mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID("master-1"),
		mink.WithEncryptedFields("UserRegistered", "email", "phone"),
		mink.WithDecryptionErrorHandler(func(err error, eventType string, metadata mink.Metadata) error {
			if errors.Is(err, encryption.ErrKeyRevoked) {
				return nil // Graceful degradation
			}
			return err
		}),
	)
	store := mink.New(adapter, mink.WithFieldEncryption(config))
	store.RegisterEvents(UserRegistered{})

	// Store encrypted data
	err := store.Append(ctx, "User-shred-1", []interface{}{
		UserRegistered{UserID: "shred-1", Name: "Jane", Email: "jane@secret.com", Phone: "+000"},
	})
	require.NoError(t, err)

	// Verify decryption works before revocation
	events, err := store.Load(ctx, "User-shred-1")
	require.NoError(t, err)
	assert.Equal(t, "jane@secret.com", events[0].Data.(UserRegistered).Email)

	// Revoke key (crypto-shredding)
	err = provider.RevokeKey("master-1")
	require.NoError(t, err)

	// Load — handler returns nil, so data is returned encrypted (graceful degradation)
	// The deserialized struct will have the base64 ciphertext in string fields
	events2, err := store.Load(ctx, "User-shred-1")
	require.NoError(t, err)
	require.Len(t, events2, 1)

	e := events2[0].Data.(UserRegistered)
	assert.Equal(t, "Jane", e.Name)                // Not encrypted
	assert.NotEqual(t, "jane@secret.com", e.Email) // Encrypted ciphertext
	assert.NotEqual(t, "+000", e.Phone)            // Encrypted ciphertext
}

// =============================================================================
// Test: Crypto-shredding without handler returns error
// =============================================================================

func TestE2E_Encryption_CryptoShredding_NoHandler(t *testing.T) {
	ctx := context.Background()
	key := testEncryptionKey(t)
	provider := local.New(local.WithKey("master-1", key))
	defer provider.Close()

	store, _ := newEncryptedStore(t, provider, "master-1")

	err := store.Append(ctx, "User-shred-2", []interface{}{
		UserRegistered{UserID: "shred-2", Name: "Bob", Email: "bob@test.com", Phone: "+111"},
	})
	require.NoError(t, err)

	// Revoke key
	err = provider.RevokeKey("master-1")
	require.NoError(t, err)

	// Without handler, load should return error
	_, err = store.Load(ctx, "User-shred-2")
	require.Error(t, err)
	assert.ErrorIs(t, err, encryption.ErrKeyRevoked)
}

// =============================================================================
// Test: Encryption combined with upcasting
// =============================================================================

func TestE2E_Encryption_WithUpcasting(t *testing.T) {
	ctx := context.Background()
	key := testEncryptionKey(t)
	provider := local.New(local.WithKey("master-1", key))
	defer provider.Close()

	adapter := memory.NewAdapter()

	chain := mink.NewUpcasterChain()
	require.NoError(t, chain.Register(&paV1ToV2{}))
	require.NoError(t, chain.Register(&paV2ToV3{}))

	config := mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID("master-1"),
		mink.WithEncryptedFields("ProductAdded", "name"),
	)

	store := mink.New(adapter,
		mink.WithUpcasters(chain),
		mink.WithFieldEncryption(config),
	)
	store.RegisterEvents(ProductAdded{})

	// Append a v3 event with encrypted name field
	err := store.Append(ctx, "ProductCatalog-enc-1", []interface{}{
		ProductAdded{ProductID: "p1", Name: "Secret Product", Price: 42.0, Currency: "USD", Tax: 5.0},
	})
	require.NoError(t, err)

	// Verify raw data has encrypted name
	raw, err := store.LoadRaw(ctx, "ProductCatalog-enc-1", 0)
	require.NoError(t, err)
	assert.NotContains(t, string(raw[0].Data), "Secret Product")

	// Load should decrypt and work
	events, err := store.Load(ctx, "ProductCatalog-enc-1")
	require.NoError(t, err)
	require.Len(t, events, 1)

	e := events[0].Data.(ProductAdded)
	assert.Equal(t, "Secret Product", e.Name)
	assert.Equal(t, 42.0, e.Price)
	assert.Equal(t, "USD", e.Currency)
}

// =============================================================================
// Test: Multiple events in single append
// =============================================================================

func TestE2E_Encryption_MultipleEvents(t *testing.T) {
	ctx := context.Background()
	key := testEncryptionKey(t)
	provider := local.New(local.WithKey("master-1", key))
	defer provider.Close()

	store, _ := newEncryptedStore(t, provider, "master-1")

	err := store.Append(ctx, "User-multi-1", []interface{}{
		UserRegistered{UserID: "m1", Name: "User1", Email: "u1@test.com", Phone: "+111"},
		UserRegistered{UserID: "m2", Name: "User2", Email: "u2@test.com", Phone: "+222"},
	})
	require.NoError(t, err)

	events, err := store.Load(ctx, "User-multi-1")
	require.NoError(t, err)
	require.Len(t, events, 2)

	assert.Equal(t, "u1@test.com", events[0].Data.(UserRegistered).Email)
	assert.Equal(t, "u2@test.com", events[1].Data.(UserRegistered).Email)

	// Each event should have its own DEK
	raw, _ := store.LoadRaw(ctx, "User-multi-1", 0)
	dek1 := raw[0].Metadata.Custom["$encrypted_dek"]
	dek2 := raw[1].Metadata.Custom["$encrypted_dek"]
	assert.NotEqual(t, dek1, dek2, "each event should have a unique DEK")
}

// =============================================================================
// Test: EventStoreWithOutbox + encryption
// =============================================================================

func TestE2E_Encryption_WithOutbox(t *testing.T) {
	ctx := context.Background()
	key := testEncryptionKey(t)
	provider := local.New(local.WithKey("master-1", key))
	defer provider.Close()

	adapter := memory.NewAdapter()
	config := mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID("master-1"),
		mink.WithEncryptedFields("UserRegistered", "email"),
	)

	store := mink.New(adapter, mink.WithFieldEncryption(config))
	store.RegisterEvents(UserRegistered{})

	routes := []mink.OutboxRoute{
		{
			EventTypes:  []string{"UserRegistered"},
			Destination: "webhook:https://example.com/events",
		},
	}

	outboxStore := memory.NewOutboxStore()
	esWithOutbox := mink.NewEventStoreWithOutbox(store, outboxStore, routes)

	// Append via outbox wrapper
	err := esWithOutbox.Append(ctx, "User-outbox-1", []interface{}{
		UserRegistered{UserID: "ob-1", Name: "Outbox User", Email: "outbox@test.com", Phone: "+333"},
	})
	require.NoError(t, err)

	// Events should be encrypted at rest
	raw, _ := store.LoadRaw(ctx, "User-outbox-1", 0)
	require.Len(t, raw, 1)
	assert.NotContains(t, string(raw[0].Data), "outbox@test.com")
	assert.True(t, mink.IsEncrypted(raw[0].Metadata))

	// Load should decrypt
	events, err := store.Load(ctx, "User-outbox-1")
	require.NoError(t, err)
	assert.Equal(t, "outbox@test.com", events[0].Data.(UserRegistered).Email)
}

// =============================================================================
// Test: SaveAggregate via outbox with encryption
// =============================================================================

func TestE2E_Encryption_OutboxSaveAggregate(t *testing.T) {
	ctx := context.Background()
	key := testEncryptionKey(t)
	provider := local.New(local.WithKey("master-1", key))
	defer provider.Close()

	adapter := memory.NewAdapter()
	config := mink.NewFieldEncryptionConfig(
		mink.WithEncryptionProvider(provider),
		mink.WithDefaultKeyID("master-1"),
		mink.WithEncryptedFields("UserRegistered", "email", "phone"),
	)

	store := mink.New(adapter, mink.WithFieldEncryption(config))
	store.RegisterEvents(UserRegistered{})

	routes := []mink.OutboxRoute{
		{Destination: "webhook:https://example.com"},
	}

	outboxStore := memory.NewOutboxStore()
	esWithOutbox := mink.NewEventStoreWithOutbox(store, outboxStore, routes)

	user := newUserProfile("outbox-agg-1")
	user.Register("Outbox Agg User", "agg@test.com", "+444")

	err := esWithOutbox.SaveAggregate(ctx, user)
	require.NoError(t, err)

	// Load aggregate through the base store
	loaded := newUserProfile("outbox-agg-1")
	err = store.LoadAggregate(ctx, loaded)
	require.NoError(t, err)
	assert.Equal(t, "agg@test.com", loaded.Email)
	assert.Equal(t, "+444", loaded.Phone)
}
