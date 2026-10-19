package service

import (
	"context"
	"errors"

	"github.com/newo-ether/filo/internal/protocol"
)

// Statuses never allocates a writer. Persisted terminal identity remains useful
// even when the live native status is unknown after executor retirement.
func (s *TaskSessions) Statuses(ctx context.Context, ids []string) ([]protocol.SessionStatus, error) {
	if len(ids) > taskSessionsListedBound {
		return nil, errors.New("At most 30 listed sessions")
	}
	statuses := make([]protocol.SessionStatus, 0, len(ids))
	for _, id := range ids {
		statuses = append(statuses, s.status(ctx, id))
	}
	return statuses, nil
}

func (s *TaskSessions) status(ctx context.Context, id string) protocol.SessionStatus {
	result := protocol.SessionStatus{ID: id}
	entry, err := s.acquire(ctx, id, false)
	if err != nil {
		return result
	}
	var runtime *protocol.Runtime
	if entry != nil {
		snapshot, err := entry.sessions.Snapshot(id)
		if err != nil {
			return result
		}
		runtime = snapshot.Page.Runtime
	} else {
		page, err := s.history.Read(ctx, id, "", false, nil)
		if err != nil {
			return result
		}
		runtime = page.Runtime
	}
	if runtime != nil {
		result.Status = listedStatus(runtime.Status)
		result.ActiveTurnID = runtime.ActiveTurnID
		if runtime.CompletedTurnID.Known {
			result.CompletedTurnID = runtime.CompletedTurnID.Value
		}
	}
	return result
}

func listedStatus(status string) *string {
	switch status {
	case "active", "idle", "ready":
		return &status
	}
	return nil
}

// mergeTaskMetadata supplements telemetry missing on a freshly attached native
// connection. Current native status and settings always take precedence.
func mergeTaskMetadata(page *protocol.ConversationPage, live protocol.ConversationPage) {
	persisted := page.Runtime
	page.Runtime = live.Runtime
	page.InputReceipts = protocol.MergeInputReceipts(page.InputReceipts, live.InputReceipts)
	if page.Runtime == nil || persisted == nil {
		return
	}
	current := *page.Runtime
	if current.ContextTokens == nil {
		current.ContextTokens = persisted.ContextTokens
	}
	if current.ContextWindow == nil {
		current.ContextWindow = persisted.ContextWindow
	}
	if !current.ServiceTier.Known && persisted.ServiceTier.Known {
		current.ServiceTier = persisted.ServiceTier
		current.ServiceTierKnown = protocol.Known(true)
	}
	if !current.Effort.Known && persisted.Effort.Known {
		current.Effort = persisted.Effort
	}
	if !current.CompletedTurnID.Known && persisted.CompletedTurnID.Known {
		current.CompletedTurnID = persisted.CompletedTurnID
	}
	page.Runtime = &current
}
