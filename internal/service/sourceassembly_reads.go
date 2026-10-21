package service

import (
	"context"
	"errors"

	"github.com/newo-ether/filo/internal/protocol"
)

// sourceReader supplies one captured scope to row identity projection, while
// the actual adapter still verifies its fresh scope before and after history.
// Only read paths use the capture. Native mutation dispatch never consults it.
type sourceReader struct {
	SessionAPI
	scope func(context.Context) (string, error)
}

func (s *sourceReader) List(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	scope, err := s.scope(ctx)
	if err != nil {
		return protocol.SessionPage{}, err
	}
	page, err := s.SessionAPI.List(withSourceReadScope(ctx, scope), cursor)
	if err != nil {
		return protocol.SessionPage{}, err
	}
	current, err := s.scope(ctx)
	if err != nil || current != scope {
		return protocol.SessionPage{}, errors.New("Native source changed during catalog read")
	}
	return page, nil
}

func (s *sourceReader) Read(ctx context.Context, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
	scope, err := s.scope(ctx)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	page, err := s.SessionAPI.Read(withSourceReadScope(ctx, scope), id, cursor, activity, excluded)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	current, err := s.scope(ctx)
	if err != nil || current != scope {
		return protocol.ConversationPage{}, errors.New("Native source changed during conversation read")
	}
	return page, nil
}

func (s *sourceReader) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	return s.SessionAPI.(SessionAccessProvider).Access(ctx, id)
}

func (s *sourceReader) CatalogAccess(ctx context.Context, id string) (protocol.SessionAccess, error) {
	scope, err := s.scope(ctx)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	return codexReadIdentity(scope, id), nil
}

func (s *sourceReader) ValidateAccess(ctx context.Context, id, binding string) error {
	return s.SessionAPI.(SessionAccessProvider).ValidateAccess(ctx, id, binding)
}

func (s *sourceReader) Statuses(ctx context.Context, ids []string) ([]protocol.SessionStatus, error) {
	return s.SessionAPI.(StatusSource).Statuses(ctx, ids)
}
