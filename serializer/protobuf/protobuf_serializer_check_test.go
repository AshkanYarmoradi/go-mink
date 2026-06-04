package protobuf_test

import (
	mink "go-mink.dev"
	"go-mink.dev/serializer/protobuf"
)

// Compile-time assertion that *protobuf.Serializer satisfies the
// mink.Serializer interface. This lives in the external test package
// (package protobuf_test) so importing the root mink package for the check
// cannot introduce an import cycle into the production build.
var _ mink.Serializer = (*protobuf.Serializer)(nil)
