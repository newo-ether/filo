package service

import (
	"context"
	"errors"

	"github.com/newo-ether/filo/internal/protocol"
)

// privateTaskSource decorates only verified native task access. TaskSessions
// remains the execution authority and rechecks ScopeValidation at dispatch.
type privateTaskSource struct {
	*TaskSessions
	scope func(context.Context) (string, error)
}

func (s *privateTaskSource) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	before, err := s.scope(ctx)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	access, err := s.TaskSessions.Access(ctx, id)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	after, err := s.scope(ctx)
	if err != nil || before != after {
		return protocol.SessionAccess{}, errors.New("Native account changed while reading task access")
	}
	identity := codexReadIdentity(before, id)
	access.NativeScopeID = before
	access.CanonicalSessionRef = identity.CanonicalSessionRef
	return access, nil
}

func (s *privateTaskSource) CatalogAccess(ctx context.Context, id string) (protocol.SessionAccess, error) {
	scope, err := s.scope(ctx)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	return codexReadIdentity(scope, id), nil
}

func (s *privateTaskSource) ValidateAccess(ctx context.Context, id, binding string) error {
	if err := protocol.ValidateScope(ctx); err != nil {
		return err
	}
	return s.TaskSessions.ValidateAccess(ctx, id, binding)
}

func privateDesktopAdapter(surface *TaskSessions, scope func(context.Context) (string, error),
	usage func(context.Context) (protocol.AccountUsage, error)) Adapter {
	return Adapter{
		ID: codexDesktopSource, Family: "codex", Origin: "desktop", Scope: scope,
		Sessions: &privateTaskSource{TaskSessions: surface, scope: scope}, Usage: usage,
		Capabilities: desktopSourceCapabilities(false),
		Status:       func() AdapterStatus { return AdapterStatus{State: "connected"} },
	}
}
