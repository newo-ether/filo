// Package service projects bounded native conversations into the public Filo
// wire format. Native ownership, transport authentication and process lifetime
// belong to their own adapters.
package service

import (
	"bytes"
	"encoding/json"
)

// Marshal mirrors JSON.stringify for the shapes this service projects: no HTML
// escaping, no trailing newline and no indentation. Lone surrogates and
// non-finite numbers are already handled by the protocol types' own marshalers.
func Marshal(value any) []byte {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		// Decoded native JSON is acyclic; null keeps size accounting finite
		// if an unsupported value nevertheless reaches the encoder.
		return []byte("null")
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
}

// Size reports the UTF-8 byte length of the JSON form, the unit the native
// service budgets pages and responses with (Buffer.byteLength).
func Size(value any) int { return len(Marshal(value)) }
