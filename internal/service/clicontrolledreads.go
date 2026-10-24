package service

import (
	"context"

	"github.com/newo-ether/filo/internal/codexcli"
	"github.com/newo-ether/filo/internal/protocol"
)

// cliControlledMetadata never allocates a native writer for metadata reads.
// Per-task settings use the admitted executor's own native model catalog.
type cliControlledMetadata struct {
	reader   *codexcli.Adapter
	metadata CLIReadMetadata
}

func (m cliControlledMetadata) Models() ([]protocol.Model, error) {
	if m.metadata != nil {
		return m.metadata.Models(context.Background())
	}
	return m.reader.Models(context.Background())
}
func (cliControlledMetadata) Rename(string, string) (protocol.Session, error) {
	return protocol.Session{}, codexcli.ErrUnsupported
}
func (cliControlledMetadata) Archive(string) (string, error) { return "", codexcli.ErrUnsupported }
func (cliControlledMetadata) PrepareShutdown() error         { return nil }

func (s *CLIControlledSessions) Models(ctx context.Context) ([]protocol.Model, error) {
	if s.metadata != nil {
		return s.metadata.Models(ctx)
	}
	return s.reader.Models(ctx)
}

func (s *CLIControlledSessions) Usage(ctx context.Context) (protocol.AccountUsage, error) {
	if s.metadata != nil {
		return s.metadata.Usage(ctx)
	}
	return s.reader.Usage(ctx)
}

func (s *CLIControlledSessions) List(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	return s.liveCatalog(ctx, cursor)
}

func (s *CLIControlledSessions) Read(ctx context.Context, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
	ctx, err := s.fenced(ctx)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	if s.isShared(id) {
		page, err := s.shared.Read(ctx, id, cursor, activity, excluded)
		if err != nil {
			return protocol.ConversationPage{}, err
		}
		access, err := s.sharedAccess(ctx, id)
		if err != nil {
			return protocol.ConversationPage{}, err
		}
		page.Access = &access
		return page, nil
	}
	owned, err := s.factory.Owns(id)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	if !owned {
		return s.reader.Read(ctx, id, cursor, activity, excluded)
	}
	page, err := s.TaskSessions.Read(ctx, id, cursor, activity, excluded)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	access, err := s.Access(ctx, id)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	page.Access = &access
	return page, nil
}

func (s *CLIControlledSessions) Attach(ctx context.Context, id string) (func(), error) {
	if _, err := s.Read(ctx, id, "", false, nil); err != nil {
		return nil, err
	}
	return func() {}, nil
}
