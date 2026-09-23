package service

import (
	"github.com/newo-ether/filo/internal/codex"
)

// CodexRpcError mirrors the native RPC error shape: a public message and the
// JSON-RPC code the HTTP layer maps to a status.
func CodexRpcError(message string, code float64) error {
	return &codex.RpcError{Message: message, Code: f64(code)}
}

func f64(value float64) *float64 { return &value }
