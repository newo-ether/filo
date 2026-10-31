package claude

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/newo-ether/filo/internal/protocol"
)

// Config choices are aliases, not provider-ready model IDs. Validate snapshots
// without treating those choices as an externally selectable model catalog.
func validateModelOptions(options []string) error {
	if len(options) > 32 {
		return errors.New("Claude native model catalog exceeds bound")
	}
	seen := map[string]bool{}
	for _, option := range options {
		if option == "" || len(option) > 256 || strings.TrimSpace(option) != option ||
			strings.IndexFunc(option, unicode.IsControl) >= 0 || seen[option] {
			return errors.New("Invalid Claude native model option")
		}
		seen[option] = true
	}
	return nil
}

func (a *Adapter) Models(ctx context.Context) ([]protocol.Model, error) {
	if err := protocol.ValidateScope(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The public Mod API exposes neither the Desktop bootstrap catalog nor a
	// read-only alias resolver. Never substitute another source's catalog.
	return nil, unsupported("model catalog")
}
