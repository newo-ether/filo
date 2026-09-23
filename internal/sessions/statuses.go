package sessions

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
)

func unknown(id string) protocol.SessionStatus { return protocol.SessionStatus{ID: id} }

func (sessions *DesktopSessions) List(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	page, err := sessions.options.History.List(ctx, cursor)
	if err != nil {
		return page, err
	}
	ids := make([]string, len(page.Sessions))
	for i, row := range page.Sessions {
		ids[i] = row.ID
	}
	if err := sessions.options.IPC.Connect(ctx); err != nil {
		page.Statuses = make([]protocol.SessionStatus, len(ids))
		for i, id := range ids {
			page.Statuses[i] = unknown(id)
		}
		return page, nil
	}
	page.Statuses, err = sessions.statuses(ctx, ids, true)
	return page, err
}

func (sessions *DesktopSessions) Statuses(ctx context.Context, ids []string) ([]protocol.SessionStatus, error) {
	return sessions.statuses(ctx, ids, false)
}

func (sessions *DesktopSessions) statuses(ctx context.Context, ids []string, listed bool) ([]protocol.SessionStatus, error) {
	if listed && len(ids) > 30 {
		return nil, errors.New("At most 30 listed sessions")
	}
	if !listed && len(ids) > 12 {
		return nil, errors.New("At most 12 visible sessions")
	}
	values := make([]protocol.SessionStatus, len(ids))
	scoped := make([]bool, len(ids))
	var next atomic.Int64
	var workers sync.WaitGroup
	for i := 0; i < min(3, len(ids)); i++ {
		workers.Go(func() {
			for {
				index := int(next.Add(1)) - 1
				if index >= len(ids) {
					return
				}
				values[index], scoped[index] = sessions.rowStatus(ctx, ids[index], listed)
			}
		})
	}
	workers.Wait()
	var createdIDs []string
	for i, id := range ids {
		if scoped[i] {
			createdIDs = append(createdIDs, id)
		}
	}
	if len(createdIDs) > 0 {
		if states, err := sessions.options.Created.Statuses(ctx, createdIDs); err == nil {
			byID := make(map[string]protocol.SessionStatus)
			for _, state := range states {
				byID[state.ID] = state
			}
			for i, id := range ids {
				if state, ok := byID[id]; scoped[i] && ok {
					values[i] = state
				}
			}
		}
	}
	return values, nil
}

func (sessions *DesktopSessions) rowStatus(ctx context.Context, id string, listed bool) (protocol.SessionStatus, bool) {
	value := unknown(id)
	owner, err := sessions.f.owner(ctx, id)
	if err != nil {
		return value, false
	}
	if owner != "" {
		entry, release, err := sessions.readState(ctx, id, owner, true)
		if err != nil || entry == nil {
			return value, false
		}
		defer release()
		sessions.f.mu.Lock()
		valid := sessions.f.entries[id] == entry && entry.owner == owner && entry.state != nil
		state := entry.state
		sessions.f.mu.Unlock()
		if !valid {
			return value, false
		}
		return codex.DesktopStatus(state), false
	}
	sessions.f.mu.Lock()
	seen := sessions.f.seen[id]
	sessions.f.mu.Unlock()
	if sessions.options.Created != nil && !seen {
		return value, true
	}
	if seen || listed {
		return value, false
	}
	page, err := sessions.options.History.Read(ctx, id, "", false, nil)
	if err == nil && page.Runtime != nil {
		runtime := page.Runtime
		if runtime.Status == "active" || runtime.Status == "idle" || runtime.Status == "ready" {
			status := runtime.Status
			value.Status = &status
			value.ActiveTurnID = runtime.ActiveTurnID
			value.CompletedTurnID = runtime.CompletedTurnID.Value
		}
	}
	return value, false
}
