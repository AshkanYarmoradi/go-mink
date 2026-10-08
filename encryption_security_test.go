package mink

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/encryption"
)

// Security-audit regression tests for field-level encryption:
//
//   - 64-bit integer fidelity: the whole event body is round-tripped through
//     map[string]interface{} while fields are sealed/unsealed, so with
//     encoding/json's float64 default every integer above 2^53 in an encrypted
//     event — sealed or not — was silently rounded. decodeJSON (UseNumber) fixes it.
//   - Decrypt integrity: $encrypted_fields only lists fields that were actually
//     sealed, so a listed field that is absent or not a string at decrypt time is
//     a tampered row and must fail closed instead of passing through as plaintext.

// Integers that float64 cannot represent exactly.
const (
	secInt64Above2p53 = "9007199254740993"     // 2^53 + 1
	secUint64Max      = "18446744073709551615" // math.MaxUint64
	secInt64Min       = "-9223372036854775808" // math.MinInt64
)

// secLargeIntEvent has its keys in sorted order so an exact byte comparison with
// json.Marshal's (sorted) output is meaningful. "ssn" and "limits" get sealed;
// "account" and "seq" are deliberately unsealed siblings.
var secLargeIntEvent = []byte(`{"account":` + secUint64Max +
	`,"limits":{"daily":` + secInt64Above2p53 + `,"note":"x"}` +
	`,"ratio":0.1,"seq":` + secInt64Above2p53 + `,"ssn":` + secInt64Min + `}`)

func TestDecodeJSON_PreservesIntegersBeyond2p53(t *testing.T) {
	// Sanity: the default decoder really does corrupt this fixture, so the
	// round-trip assertions in the other tests are load-bearing.
	var lossy map[string]interface{}
	require.NoError(t, json.Unmarshal(secLargeIntEvent, &lossy))
	lossyBytes, err := json.Marshal(lossy)
	require.NoError(t, err)
	assert.NotEqual(t, string(secLargeIntEvent), string(lossyBytes), "fixture must exceed float64 precision")

	var exact map[string]interface{}
	require.NoError(t, decodeJSON(secLargeIntEvent, &exact))
	exactBytes, err := json.Marshal(exact)
	require.NoError(t, err)
	assert.Equal(t, string(secLargeIntEvent), string(exactBytes))
	assert.IsType(t, json.Number(""), exact["seq"])
}

func TestDecodeJSON_RejectsInvalidAndTrailingInput(t *testing.T) {
	var v interface{}
	assert.Error(t, decodeJSON([]byte(`not json`), &v))
	assert.Error(t, decodeJSON([]byte(``), &v))
	assert.Error(t, decodeJSON([]byte(`{"a":1} trailing`), &v), "trailing data must be rejected like json.Unmarshal does")
	assert.Error(t, decodeJSON([]byte(`{"a":1}{"b":2}`), &v), "a second top-level value must be rejected")
	assert.NoError(t, decodeJSON([]byte(` {"a":1} `), &v), "surrounding whitespace is fine")
}

func TestFieldEncryptionConfig_EncryptDecryptFields_PreservesLargeIntegers(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("LedgerOpened", "ssn", "limits"))
	ctx := context.Background()

	encData, encMeta, err := config.encryptFields(ctx, "Ledger-1", "LedgerOpened", secLargeIntEvent, Metadata{})
	require.NoError(t, err)
	require.True(t, IsEncrypted(encMeta))
	assert.ElementsMatch(t, []string{"ssn", "limits"}, GetEncryptedFields(encMeta))

	// Unsealed siblings are stored byte-for-byte; sealed fields are ciphertext strings.
	assert.Contains(t, string(encData), `"account":`+secUint64Max)
	assert.Contains(t, string(encData), `"seq":`+secInt64Above2p53)
	assert.NotContains(t, string(encData), secInt64Min, "sealed int64 must not appear in plaintext")
	assert.NotContains(t, string(encData), `"daily"`, "sealed nested object must not appear in plaintext")
	var stored map[string]interface{}
	require.NoError(t, decodeJSON(encData, &stored))
	assert.IsType(t, "", stored["ssn"])
	assert.IsType(t, "", stored["limits"])

	decData, err := config.decryptFields(ctx, "Ledger-1", "LedgerOpened", encData, encMeta)
	require.NoError(t, err)
	assert.Equal(t, string(secLargeIntEvent), string(decData), "decrypted body must equal the original byte-for-byte")
}

type secLedgerLimits struct {
	Daily int64  `json:"daily"`
	Note  string `json:"note"`
}

type secLedgerOpened struct {
	Account uint64          `json:"account"`
	Limits  secLedgerLimits `json:"limits"`
	Ratio   float64         `json:"ratio"`
	Seq     int64           `json:"seq"`
	SSN     int64           `json:"ssn"`
}

func TestEventStore_FieldEncryption_PreservesLargeIntegersEndToEnd(t *testing.T) {
	provider := testProvider(t, "master-1")
	t.Cleanup(func() { _ = provider.Close() })
	config := NewFieldEncryptionConfig(
		WithEncryptionProvider(provider),
		WithDefaultKeyID("master-1"),
		WithEncryptedFields("secLedgerOpened", "ssn", "limits"),
	)
	adapter := memory.NewAdapter()
	store := New(adapter, WithFieldEncryption(config))
	store.RegisterEvents(secLedgerOpened{})
	ctx := context.Background()

	want := secLedgerOpened{
		Account: math.MaxUint64,
		Limits:  secLedgerLimits{Daily: 1<<53 + 1, Note: "x"},
		Ratio:   0.1,
		Seq:     1<<53 + 1,
		SSN:     math.MinInt64,
	}
	require.NoError(t, store.Append(ctx, "Ledger-1", []interface{}{want}))

	// At rest: the unsealed uint64 is verbatim and the sealed int64 is absent.
	raw, err := adapter.Load(ctx, "Ledger-1", 0)
	require.NoError(t, err)
	require.Len(t, raw, 1)
	assert.Contains(t, string(raw[0].Data), `"account":`+secUint64Max)
	assert.NotContains(t, string(raw[0].Data), secInt64Min)

	events, err := store.Load(ctx, "Ledger-1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	got, ok := events[0].Data.(secLedgerOpened)
	require.True(t, ok, "expected secLedgerOpened, got %T", events[0].Data)
	assert.Equal(t, want, got)
}

func TestDecryptFields_TamperedEncryptedField_FailsClosed(t *testing.T) {
	_, config := testEncConfig(t, "master-1", WithEncryptedFields("UserCreated", "email", "address.street"))
	ctx := context.Background()

	original := []byte(`{"address":{"city":"Berlin","street":"Main St 1"},"email":"a@example.com","name":"Ann"}`)
	encData, encMeta, err := config.encryptFields(ctx, "User-1", "UserCreated", original, Metadata{})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"email", "address.street"}, GetEncryptedFields(encMeta))

	tests := []struct {
		name      string
		mutate    func(m map[string]interface{})
		wantField string
	}{
		{"leaf removed", func(m map[string]interface{}) { delete(m, "email") }, "email"},
		{"leaf replaced with number", func(m map[string]interface{}) { m["email"] = 42 }, "email"},
		{"leaf replaced with object", func(m map[string]interface{}) { m["email"] = map[string]interface{}{"x": "y"} }, "email"},
		{"leaf replaced with null", func(m map[string]interface{}) { m["email"] = nil }, "email"},
		{"nested leaf removed", func(m map[string]interface{}) { delete(m["address"].(map[string]interface{}), "street") }, "address.street"},
		{"nested parent removed", func(m map[string]interface{}) { delete(m, "address") }, "address.street"},
		{"nested parent flattened to string", func(m map[string]interface{}) { m["address"] = "Main St 1, Berlin" }, "address.street"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m map[string]interface{}
			require.NoError(t, decodeJSON(encData, &m))
			tt.mutate(m)
			tampered, err := json.Marshal(m)
			require.NoError(t, err)

			out, err := config.decryptFields(ctx, "User-1", "UserCreated", tampered, encMeta)
			require.Error(t, err)
			assert.Nil(t, out, "no partially decrypted body may escape")
			assert.ErrorIs(t, err, encryption.ErrDecryptionFailed)
			assert.ErrorIs(t, err, ErrDecryptionFailed)
			var ee *encryption.EncryptionError
			require.True(t, errors.As(err, &ee), "got %T", err)
			assert.Equal(t, tt.wantField, ee.Field)
			assert.Equal(t, "master-1", ee.KeyID)
			assert.Contains(t, err.Error(), "tampering")
		})
	}

	// Control: the untampered row still decrypts to the original.
	decData, err := config.decryptFields(ctx, "User-1", "UserCreated", encData, encMeta)
	require.NoError(t, err)
	assert.Equal(t, string(original), string(decData))
}
