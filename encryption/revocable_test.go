package encryption

import (
	"context"
	"errors"
	"testing"
)

// noopProvider implements Provider with no-op methods, for composing test doubles.
type noopProvider struct{}

func (noopProvider) Encrypt(context.Context, string, []byte) ([]byte, error)        { return nil, nil }
func (noopProvider) Decrypt(context.Context, string, []byte) ([]byte, error)        { return nil, nil }
func (noopProvider) GenerateDataKey(context.Context, string) (*DataKey, error)      { return nil, nil }
func (noopProvider) DecryptDataKey(context.Context, string, []byte) ([]byte, error) { return nil, nil }
func (noopProvider) Close() error                                                   { return nil }

// nonRevocable implements Provider but NOT Revocable.
type nonRevocable struct{ noopProvider }

// revocableDouble implements Provider AND Revocable.
type revocableDouble struct {
	noopProvider
	revoked map[string]bool
}

func (r *revocableDouble) RevokeKey(keyID string) error {
	if r.revoked == nil {
		r.revoked = map[string]bool{}
	}
	r.revoked[keyID] = true
	return nil
}

func (r *revocableDouble) IsRevoked(keyID string) (bool, error) { return r.revoked[keyID], nil }

func TestRevoke_UnsupportedProvider(t *testing.T) {
	if err := Revoke(nonRevocable{}, "k"); !errors.Is(err, ErrRevocationUnsupported) {
		t.Fatalf("Revoke: want ErrRevocationUnsupported, got %v", err)
	}
	if _, err := IsRevoked(nonRevocable{}, "k"); !errors.Is(err, ErrRevocationUnsupported) {
		t.Fatalf("IsRevoked: want ErrRevocationUnsupported, got %v", err)
	}
}

func TestRevoke_DelegatesToRevocable(t *testing.T) {
	r := &revocableDouble{}

	if err := Revoke(r, "k"); err != nil {
		t.Fatalf("Revoke: unexpected error %v", err)
	}
	got, err := IsRevoked(r, "k")
	if err != nil {
		t.Fatalf("IsRevoked: unexpected error %v", err)
	}
	if !got {
		t.Fatalf("IsRevoked: want true after Revoke")
	}
}
