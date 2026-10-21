package codexcli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/newo-ether/filo/internal/protocol"
)

// Read cursors carry the verified scope so account changes invalidate old pages.
// The public service additionally authenticates and source-binds these tokens.
type historyCursor struct {
	Scope   string `json:"scope"`
	Session string `json:"session"`
	Native  string `json:"native"`
}

func (a *Adapter) unwrapCursor(ctx context.Context, id, cursor string) (string, string, error) {
	scope, err := a.scope(ctx)
	if err != nil {
		return "", "", err
	}
	if cursor == "" {
		return scope, "", nil
	}
	if len(cursor) > 16384 {
		return "", "", errors.New("Invalid Codex CLI history cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	var value historyCursor
	if err != nil || json.Unmarshal(data, &value) != nil || value.Scope != scope || value.Session != id || len(value.Native) > 8192 {
		return "", "", errors.New("Codex CLI cursor does not match verified store/session")
	}
	return scope, value.Native, nil
}

func wrapCursor(scope, id string, cursor *string) (*string, error) {
	if cursor == nil || *cursor == "" {
		return nil, nil
	}
	if len(*cursor) > 8192 {
		return nil, errors.New("Native Codex history cursor is too large")
	}
	data, _ := json.Marshal(historyCursor{Scope: scope, Session: id, Native: *cursor})
	value := base64.RawURLEncoding.EncodeToString(data)
	return &value, nil
}

func (a *Adapter) sameScope(ctx context.Context, before string) error {
	after, err := a.scope(ctx)
	if err != nil {
		return err
	}
	if before != after {
		return ErrUnavailable
	}
	return nil
}

func (a *Adapter) List(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	scope, native, err := a.unwrapCursor(ctx, "", cursor)
	if err != nil {
		return protocol.SessionPage{}, err
	}
	page, err := a.options.History.List(ctx, native)
	if err != nil {
		return protocol.SessionPage{}, err
	}
	if len(page.Sessions) > 30 || len(page.Statuses) > 30 {
		return protocol.SessionPage{}, errors.New("Native Codex catalog exceeds page bound")
	}
	seen := make(map[string]bool, len(page.Sessions))
	for i := range page.Sessions {
		id := page.Sessions[i].ID
		if !nativeIDPattern.MatchString(id) || seen[id] {
			return protocol.SessionPage{}, errors.New("Invalid native Codex catalog identity")
		}
		seen[id] = true
		access, err := a.identity(ctx, scope, id)
		if err != nil {
			return protocol.SessionPage{}, err
		}
		page.Sessions[i].Access = &access
	}
	for _, status := range page.Statuses {
		if !seen[status.ID] {
			return protocol.SessionPage{}, errors.New("Invalid native Codex catalog status identity")
		}
	}
	if err := a.sameScope(ctx, scope); err != nil {
		return protocol.SessionPage{}, err
	}
	page.NextCursor, err = wrapCursor(scope, "", page.NextCursor)
	return page, err
}

func (a *Adapter) Read(ctx context.Context, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
	if !nativeIDPattern.MatchString(id) || len(excluded) > 128 {
		return protocol.ConversationPage{}, errors.New("Invalid native Codex history request")
	}
	scope, native, err := a.unwrapCursor(ctx, id, cursor)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	page, err := a.options.History.Read(ctx, id, native, activity, excluded)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	if len(page.Messages) > 128 || len(page.InputReceipts) > 128 || len(page.Queued) != 0 {
		return protocol.ConversationPage{}, errors.New("Native Codex history exceeds page bound")
	}
	for _, message := range page.Messages {
		if message.ID == "" || message.TurnID == "" {
			return protocol.ConversationPage{}, errors.New("Native Codex message identity is unavailable")
		}
	}
	access, err := a.identity(ctx, scope, id)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	if err := a.sameScope(ctx, scope); err != nil {
		return protocol.ConversationPage{}, err
	}
	page.Access = &access
	page.NextCursor, err = wrapCursor(scope, id, page.NextCursor)
	return page, err
}

// Statuses observes persisted terminal evidence through the existing bounded
// reader. It never infers idle from daemon absence or creates a live follower.
func (a *Adapter) Statuses(ctx context.Context, ids []string) ([]protocol.SessionStatus, error) {
	if len(ids) == 0 || len(ids) > 12 {
		return nil, errors.New("Expected 1-12 native Codex sessions")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !nativeIDPattern.MatchString(id) || seen[id] {
			return nil, errors.New("Invalid native Codex status identity")
		}
		seen[id] = true
	}
	scope, err := a.scope(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]protocol.SessionStatus, 0, len(ids))
	for _, id := range ids {
		page, err := a.Read(ctx, id, "", false, nil)
		if err != nil {
			return nil, err
		}
		status := protocol.SessionStatus{ID: id}
		if page.Runtime != nil {
			runtime := page.Runtime
			status.Status = &runtime.Status
			status.ActiveTurnID = runtime.ActiveTurnID
			if runtime.CompletedTurnID.Known {
				status.CompletedTurnID = runtime.CompletedTurnID.Value
			}
		}
		statuses = append(statuses, status)
	}
	if err := a.sameScope(ctx, scope); err != nil {
		return nil, err
	}
	return statuses, nil
}
