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

// statefulDouble implements StatefulRevocable (and Revocable).
type statefulDouble struct {
	noopProvider
	state map[string]RevocationState
}

func (s *statefulDouble) RevokeKey(keyID string) error {
	if s.state == nil {
		s.state = map[string]RevocationState{}
	}
	s.state[keyID] = Revoked
	return nil
}
func (s *statefulDouble) IsRevoked(keyID string) (bool, error) {
	return s.state[keyID] == Revoked || s.state[keyID] == SoftRevoked, nil
}
func (s *statefulDouble) RevocationState(keyID string) (RevocationState, error) {
	return s.state[keyID], nil
}

func TestGetRevocationState_FallbackToIsRevoked(t *testing.T) {
	// A Revocable-but-not-Stateful provider maps IsRevoked → Revoked/NotRevoked,
	// and can therefore never report SoftRevoked.
	r := &revocableDouble{}
	if st, err := GetRevocationState(r, "k"); err != nil || st != NotRevoked {
		t.Fatalf("want NotRevoked/nil, got %v/%v", st, err)
	}
	_ = Revoke(r, "k")
	if st, err := GetRevocationState(r, "k"); err != nil || st != Revoked {
		t.Fatalf("want Revoked/nil, got %v/%v", st, err)
	}
}

func TestGetRevocationState_UsesStateful(t *testing.T) {
	s := &statefulDouble{state: map[string]RevocationState{"k": SoftRevoked}}
	if st, err := GetRevocationState(s, "k"); err != nil || st != SoftRevoked {
		t.Fatalf("want SoftRevoked/nil, got %v/%v", st, err)
	}
}

func TestSoftRevoke_Unrevoke_Unsupported(t *testing.T) {
	// Revocable but NOT RecoverableRevocable → soft helpers report unsupported,
	// so a caller relying on a grace window is told, not silently hard-revoked.
	r := &revocableDouble{}
	if err := SoftRevoke(r, "k", 0); !errors.Is(err, ErrRevocationUnsupported) {
		t.Fatalf("SoftRevoke: want ErrRevocationUnsupported, got %v", err)
	}
	if err := Unrevoke(r, "k"); !errors.Is(err, ErrRevocationUnsupported) {
		t.Fatalf("Unrevoke: want ErrRevocationUnsupported, got %v", err)
	}
	// Not even Revocable → also unsupported.
	if err := SoftRevoke(nonRevocable{}, "k", 0); !errors.Is(err, ErrRevocationUnsupported) {
		t.Fatalf("SoftRevoke(non-revocable): want ErrRevocationUnsupported, got %v", err)
	}
}
