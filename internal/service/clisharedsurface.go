package service

import (
	"context"
	"sort"
	"strconv"

	"github.com/newo-ether/filo/internal/codexcli"
	"github.com/newo-ether/filo/internal/protocol"
)

func (s *CLIControlledSessions) Statuses(ctx context.Context, ids []string) ([]protocol.SessionStatus, error) {
	if len(ids) > 30 {
		return nil, codexcli.ErrUnavailable
	}
	result := make([]protocol.SessionStatus, 0, len(ids))
	for _, id := range ids {
		if s.isShared(id) {
			result = append(result, s.shared.status(ctx, id))
		} else {
			result = append(result, s.TaskSessions.status(ctx, id))
		}
	}
	return result, nil
}

func (s *CLIControlledSessions) sharedAccess(ctx context.Context, id string) (protocol.SessionAccess, error) {
	scope, err := s.scope(ctx)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	access, err := s.shared.Access(ctx, id)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	identity := codexReadIdentity(scope, id)
	caps := s.Capabilities()
	access.Capabilities.Models, access.Capabilities.Usage = caps.Models, caps.Usage
	access.NativeScopeID, access.CanonicalSessionRef = scope, identity.CanonicalSessionRef
	access.Binding = s.shared.taskBinding(id, "cli-shared-scope", scope, access.Binding)
	after, err := s.scope(ctx)
	if err != nil || after != scope {
		return protocol.SessionAccess{}, codexcli.ErrUnavailable
	}
	return access, protocol.ValidateScope(ctx)
}

func (s *CLIControlledSessions) sharedContext(ctx context.Context, id string) (context.Context, error) {
	ctx, err := s.fenced(ctx)
	if err != nil {
		return nil, err
	}
	access, err := s.sharedAccess(ctx, id)
	if err != nil {
		return nil, err
	}
	binding := protocol.AccessBinding(ctx)
	if !access.Capabilities.Live || (binding != "" && binding != access.Binding) {
		return nil, taskAccessChanged()
	}
	inner, err := s.shared.Access(ctx, id)
	if err != nil || s.shared.taskBinding(id, "cli-shared-scope", access.NativeScopeID, inner.Binding) != access.Binding {
		return nil, taskAccessChanged()
	}
	return protocol.WithAccessBinding(ctx, inner.Binding), nil
}

func (s *CLIControlledSessions) isShared(id string) bool {
	if s.sharedFactory == nil {
		return false
	}
	ok, _ := s.sharedFactory.Owns(id)
	return ok
}

// liveCatalog never calls the historical List owner. Existing history Read is
// retained for exact identities, independently of main-catalog visibility.
func (s *CLIControlledSessions) liveCatalog(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	ctx, err := s.fenced(ctx)
	if err != nil {
		return protocol.SessionPage{}, err
	}
	rows := []protocol.Session{}
	seen := map[string]bool{}
	if s.sharedFactory != nil {
		shared, err := s.sharedFactory.catalog(ctx)
		if err != nil {
			return protocol.SessionPage{}, err
		}
		s.pruneSharedEntries()
		for _, row := range shared {
			if _, err := s.shared.acquire(ctx, row.ID, false); err != nil {
				continue
			}
			access, err := s.sharedAccess(ctx, row.ID)
			if err != nil || !access.Capabilities.Live {
				continue
			}
			row.Access = &access
			rows = append(rows, row)
			seen[row.ID] = true
		}
	}
	ids, err := s.liveOwnedIDs(ctx)
	if err != nil {
		return protocol.SessionPage{}, err
	}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if _, err := s.TaskSessions.acquire(ctx, id, false); err != nil {
			continue
		}
		access, err := s.Access(ctx, id)
		if err != nil || !access.Capabilities.Live {
			continue
		}
		s.mu.Lock()
		entry := s.entries[id]
		s.mu.Unlock()
		if entry == nil {
			continue
		}
		value, err := entry.native.Request("thread/read", map[string]any{"threadId": id, "includeTurns": false})
		if err != nil {
			continue
		}
		row, err := cliLiveSession(value)
		if err != nil {
			continue
		}
		row.Access = &access
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].UpdatedAt == rows[j].UpdatedAt {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].UpdatedAt > rows[j].UpdatedAt
	})
	start := 0
	if cursor != "" {
		start, err = strconv.Atoi(cursor)
		if err != nil || start < 0 || start > len(rows) {
			return protocol.SessionPage{}, codexcli.ErrUnavailable
		}
	}
	end := start + 30
	var next *string
	if end < len(rows) {
		value := strconv.Itoa(end)
		next = &value
	} else {
		end = len(rows)
	}
	return protocol.SessionPage{Sessions: rows[start:end], NextCursor: next}, protocol.ValidateScope(ctx)
}

func (s *CLIControlledSessions) pruneSharedEntries() {
	s.shared.mu.Lock()
	removed := []*taskSessionsEntry{}
	for id, entry := range s.shared.entries {
		if owned, _ := s.sharedFactory.Owns(id); !owned {
			delete(s.shared.entries, id)
			removed = append(removed, entry)
		}
	}
	s.shared.mu.Unlock()
	for _, entry := range removed {
		entry.connection.Close()
	}
}
