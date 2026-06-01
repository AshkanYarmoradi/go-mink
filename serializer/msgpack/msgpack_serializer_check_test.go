package msgpack_test

import (
	mink "go-mink.dev"
	"go-mink.dev/serializer/msgpack"
)

// Compile-time assertion that *msgpack.Serializer satisfies the mink.Serializer
// interface. It lives in the external test package (package msgpack_test) so
// importing the root mink package for the check cannot introduce an import
// cycle into the production build.
var _ mink.Serializer = (*msgpack.Serializer)(nil)
