package sessions

import (
	"context"

	"github.com/newo-ether/filo/internal/protocol"
)

type idleRecoveryKey struct{}

// WithIdleRecovery admits only the named task for this explicit mutation. It is
// never attached to browsing or stop requests and must be revalidated by the
// private helper before opening a writer.
func WithIdleRecovery(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, idleRecoveryKey{}, id)
}

// IdleRecoveryAllowed reports whether this mutation carries exact-task admission.
func IdleRecoveryAllowed(ctx context.Context, id string) bool {
	value, _ := ctx.Value(idleRecoveryKey{}).(string)
	return id != "" && value == id
}

// RecoverableRuntime requires positive native evidence, not merely a missing
// owner. Older history projections may retain notLoaded with an exact terminal.
func RecoverableRuntime(runtime *protocol.Runtime) bool {
	if runtime == nil || runtime.ActiveTurnID != nil {
		return false
	}
	if runtime.Status == "idle" {
		return true
	}
	return runtime.Status == "notLoaded" && runtime.CompletedTurnID.Known &&
		runtime.CompletedTurnID.Value != nil && *runtime.CompletedTurnID.Value != ""
}

func (sessions *DesktopSessions) mutationDestination(ctx context.Context, id string) (string, context.Context, error) {
	admitted, err := sessions.admitAccess(ctx, id)
	if err != nil {
		return "", ctx, err
	}
	ctx = admitted
	if owner, scoped, err := sessions.scopedDestination(ctx, id); err != nil || scoped {
		return owner, ctx, err
	}
	owner, err := sessions.f.owner(ctx, id)
	if err != nil || owner != "" {
		return owner, ctx, err
	}
	created := false
	scope, hasCreationScope := sessions.options.Created.(interface {
		Created(context.Context, string) (bool, error)
	})
	if sessions.options.Created != nil {
		if hasCreationScope {
			created, err = scope.Created(ctx, id)
			if err != nil {
				return "", ctx, err
			}
		} else {
			created, err = sessions.created(ctx, id)
			if err != nil {
				return "", ctx, err
			}
		}
	}
	if !created || hasCreationScope {
		if sessions.options.Created == nil || sessions.options.History == nil {
			return "", ctx, unavailable()
		}
		owned := created
		// A legacy ownership interface cannot distinguish recovery from a task
		// previously seen on Desktop, so it cannot prove a recovered executor.
		if !created && hasCreationScope {
			owned, err = sessions.options.Created.Owns(ctx, id)
			if err != nil {
				return "", ctx, err
			}
		}
		var page protocol.ConversationPage
		if owned {
			// A durable recovery reference permits reading the existing executor,
			// but does not prove that a replacement writer may be allocated.
			page, err = sessions.options.Created.Read(ctx, id, "", false)
		} else {
			page, err = sessions.options.History.Read(ctx, id, "", false, nil)
		}
		if err != nil {
			return "", ctx, err
		}
		runtime := page.Runtime
		active := owned && runtime != nil && runtime.Status == "active" &&
			runtime.ActiveTurnID != nil && *runtime.ActiveTurnID != ""
		if !active {
			if !RecoverableRuntime(runtime) {
				return "", ctx, unavailable()
			}
			ctx = WithIdleRecovery(ctx, id)
		}
	}
	owner, err = sessions.f.owner(ctx, id)
	if err != nil {
		return "", ctx, err
	}
	if owner != "" {
		return "", ctx, unavailable()
	}
	return "", ctx, ctx.Err()
}
