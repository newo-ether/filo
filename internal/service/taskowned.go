package service

import (
	"context"

	"github.com/newo-ether/filo/internal/protocol"
)

// RenameOwned renames one Filo-created task through its own admitted native
// connection. Unlike Rename it never uses a peripheral metadata surface, so a
// source without one (Codex CLI) still renames on the exact native owner.
func (s *TaskSessions) RenameOwned(ctx context.Context, id, name string) (protocol.Session, error) {
	var renamed protocol.Session
	err := s.mutation(func() error {
		entry, release, err := s.ownedEntry(ctx, id)
		if err != nil {
			return err
		}
		defer release()
		renamed, err = entry.sessions.RenameSession(id, name)
		return err
	})
	if err != nil {
		return protocol.Session{}, err
	}
	return renamed, nil
}

// ArchiveOwned archives one Filo-created task on its own admitted connection,
// admitting an idle task first rather than falling back to peripheral metadata.
func (s *TaskSessions) ArchiveOwned(ctx context.Context, id string) (string, error) {
	var cwd string
	err := s.mutation(func() error {
		entry, release, err := s.ownedEntry(ctx, id)
		if err != nil {
			return err
		}
		defer release()
		cwd, err = entry.sessions.ArchiveSession(id)
		return err
	})
	if err != nil {
		return "", err
	}
	return cwd, nil
}

func (s *TaskSessions) ownedEntry(ctx context.Context, id string) (*taskSessionsEntry, func(), error) {
	entry, err := s.acquire(ctx, id, true)
	if err != nil {
		return nil, nil, err
	}
	if entry == nil {
		return nil, nil, notReady()
	}
	release, err := s.bindTaskMutation(ctx, id, entry)
	if err != nil {
		return nil, nil, err
	}
	return entry, release, nil
}
