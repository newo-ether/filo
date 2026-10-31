package sessions

import (
	"context"

	"github.com/newo-ether/filo/internal/protocol"
)

// Catalog management has no execution owner. Its account and exact-row binding
// are checked at dispatch without loading a follower or admitting a writer.
func (sessions *DesktopSessions) validateMetadataAccess(ctx context.Context, id string) error {
	if err := protocol.ValidateScope(ctx); err != nil {
		return err
	}
	if binding := sessions.options.AccessBinding(ctx); binding != "" {
		return sessions.ValidateAccess(ctx, id, binding)
	}
	return ctx.Err()
}
