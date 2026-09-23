package protocol

import "github.com/newo-ether/filo/internal/nativejson"

// Text round-trips native UTF-16 text through public JSON without replacing lone
// surrogates. Normal text remains UTF-8; opaque native code units use WTF-8 inside
// Go and escaped UTF-16 on the wire. Convert to string only for local processing.
type Text = nativejson.SurrogateText
