package mink_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mink "go-mink.dev"
	"go-mink.dev/adapters"
	"go-mink.dev/adapters/memory"
	"go-mink.dev/serializer/msgpack"
)

// e2eProduct is the LATEST (v2) shape of the product event; v1 had only Name.
type e2eProduct struct {
	Name  string `json:"name"`
	Price int    `json:"price"`
}

type e2eProductV1ToV2 struct{}

func (e2eProductV1ToV2) EventType() string { return "e2eProduct" }
func (e2eProductV1ToV2) FromVersion() int  { return 1 }
func (e2eProductV1ToV2) ToVersion() int    { return 2 }
func (e2eProductV1ToV2) Upcast(data []byte, _ mink.Metadata) ([]byte, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if _, ok := m["price"]; !ok {
		m["price"] = 0
	}
	return json.Marshal(m)
}

type e2eProductV2ToV3 struct{}

func (e2eProductV2ToV3) EventType() string { return "e2eProduct" }
func (e2eProductV2ToV3) FromVersion() int  { return 2 }
func (e2eProductV2ToV3) ToVersion() int    { return 3 }
func (e2eProductV2ToV3) Upcast(data []byte, _ mink.Metadata) ([]byte, error) {
	return data, nil
}

// seedV1 writes a v1 product event straight through the adapter (no $schema_version stamped).
func seedV1(t *testing.T, p *e2ePG, streamID, name string) {
	t.Helper()
	_, err := p.Adapter.Append(p.Ctx, streamID,
		[]adapters.EventRecord{{Type: "e2eProduct", Data: []byte(`{"name":"` + name + `"}`)}},
		mink.AnyVersion)
	require.NoError(t, err)
}

// TestE2E_Serialization_UpcastOnLoadFromPG: a v1 event stored in PostgreSQL is upcast to v2 on
// Load, the stored row stays v1 (history not rewritten), and new appends stamp the latest version.
func TestE2E_Serialization_UpcastOnLoadFromPG(t *testing.T) {
	p := newE2EPGBase(t)
	chain := mink.NewUpcasterChain()
	require.NoError(t, chain.Register(e2eProductV1ToV2{}))
	p.Store = mink.New(p.Adapter, mink.WithUpcasters(chain))
	p.Store.RegisterEvents(e2eProduct{})

	seedV1(t, p, "product-1", "widget")

	// Load upcasts the v1 event to the v2 struct (Price added by the upcaster).
	events, err := p.Store.Load(p.Ctx, "product-1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	prod, ok := events[0].Data.(e2eProduct)
	require.True(t, ok, "event upcast+deserialized to the latest struct, got %T", events[0].Data)
	assert.Equal(t, "widget", prod.Name)
	assert.Equal(t, 0, prod.Price)

	// The stored row is unchanged: still v1 on disk, no `price` field written.
	raw, err := p.Store.LoadRaw(p.Ctx, "product-1", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, mink.GetSchemaVersion(raw[0].Metadata), "history not rewritten: stored event is still v1")
	assert.NotContains(t, string(raw[0].Data), "price")

	// A new append through the upcaster-configured store stamps the latest schema version (2).
	require.NoError(t, p.Store.Append(p.Ctx, "product-2", []interface{}{e2eProduct{Name: "gadget", Price: 5}}))
	raw2, err := p.Store.LoadRaw(p.Ctx, "product-2", 0)
	require.NoError(t, err)
	assert.Equal(t, 2, mink.GetSchemaVersion(raw2[0].Metadata), "new events stamped with the latest version")
}

// TestE2E_Serialization_SchemaGapErrorsOnLoad: a missing upcaster (v1->v2) surfaces a typed
// schema-version-gap error on Load rather than returning malformed data.
func TestE2E_Serialization_SchemaGapErrorsOnLoad(t *testing.T) {
	p := newE2EPGBase(t)
	chain := mink.NewUpcasterChain()
	require.NoError(t, chain.Register(e2eProductV2ToV3{})) // v1->v2 missing => gap
	p.Store = mink.New(p.Adapter, mink.WithUpcasters(chain))
	p.Store.RegisterEvents(e2eProduct{})

	seedV1(t, p, "product-gap", "widget")

	_, err := p.Store.Load(p.Ctx, "product-gap")
	require.Error(t, err)
	assert.ErrorIs(t, err, mink.ErrSchemaVersionGap)
}

// e2eProductMsg is an msgpack-tagged event.
type e2eProductMsg struct {
	Name  string `msgpack:"name"`
	Price int    `msgpack:"price"`
}

// TestE2E_Serialization_MsgpackRoundTrip: a msgpack-serialized event round-trips through an event
// store and reloads as the correct concrete type. It runs on the in-memory adapter: the
// PostgreSQL adapter's `data` column is JSONB, which rejects non-JSON (msgpack/protobuf) bodies —
// see TestE2E_Serialization_MsgpackRejectedByPGJSONB for that documented constraint.
func TestE2E_Serialization_MsgpackRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode for consistency with the E2E suite")
	}
	s := msgpack.NewSerializer()
	s.Register("e2eProductMsg", e2eProductMsg{})
	store := mink.New(memory.NewAdapter(), mink.WithSerializer(s))

	require.NoError(t, store.Append(t.Context(), "prod-msg", []interface{}{e2eProductMsg{Name: "widget", Price: 9}}))
	loaded, err := store.Load(t.Context(), "prod-msg")
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	prod, ok := loaded[0].Data.(e2eProductMsg)
	require.True(t, ok, "msgpack event reloaded as its concrete type, got %T", loaded[0].Data)
	assert.Equal(t, "widget", prod.Name)
	assert.Equal(t, 9, prod.Price)
}

// TestE2E_Serialization_MsgpackRejectedByPGJSONB locks in the fail-fast contract: because the
// PostgreSQL adapter stores event data in a JSONB column, pairing it with a binary serializer
// (msgpack) is rejected with the clear ErrBinarySerializerUnsupported on the first Append —
// before any INSERT — instead of a cryptic "invalid input syntax for type json" from the driver.
// mink.New records the incompatibility rather than panicking (a library constructor must not
// panic on a recoverable misconfiguration), so the real PG adapter here is never actually hit.
func TestE2E_Serialization_MsgpackRejectedByPGJSONB(t *testing.T) {
	p := newE2EPGBase(t)
	s := msgpack.NewSerializer()
	s.Register("e2eProductMsg", e2eProductMsg{})

	store := mink.New(p.Adapter, mink.WithSerializer(s))
	err := store.Append(p.Ctx, "prod-msg", []interface{}{e2eProductMsg{Name: "widget", Price: 9}})
	require.Error(t, err, "expected msgpack + JSONB PostgreSQL adapter to reject the append")
	assert.ErrorIs(t, err, mink.ErrBinarySerializerUnsupported)
}
