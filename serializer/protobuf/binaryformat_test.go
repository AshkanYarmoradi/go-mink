package protobuf

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSerializer_BinaryFormat asserts the protobuf serializer reports a binary output format, so
// mink.New rejects pairing it with a JSON/JSONB-backed adapter.
func TestSerializer_BinaryFormat(t *testing.T) {
	assert.True(t, NewSerializer().BinaryFormat())
}
