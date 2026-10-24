package claude

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode"

	"github.com/newo-ether/filo/internal/protocol"
)

// Only the engine's bounded model row is admitted. Other config rows, including
// plugin rows, never become a catalog or an invented effort list.
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
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked()
	ids := make([]string, 0, len(a.registrations))
	for id := range a.registrations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	seen := map[string]bool{}
	models := []protocol.Model{}
	for _, id := range ids {
		for _, option := range a.registrations[id].modelOptions {
			if !seen[option] {
				seen[option] = true
				models = append(models, protocol.Model{ID: option, Name: protocol.Text(option), ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"}})
			}
		}
	}
	return models, nil
}
