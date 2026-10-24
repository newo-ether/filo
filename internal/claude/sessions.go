package claude

import (
	"context"
	"errors"
	"sort"

	"github.com/newo-ether/filo/internal/protocol"
)

// HistoryBoundary is explicit: these rows are observed after Mod registration,
// not a recovered native transcript. A registration starts with no stable history.
const HistoryBoundary = "observed-since-registration"

func (a *Adapter) List(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionPage{}, err
	}
	if cursor != "" {
		return protocol.SessionPage{}, errors.New("Claude Mod catalog has no older page")
	}
	a.mu.Lock()
	a.pruneLocked()
	titles := make([]string, 0, len(a.registrations))
	for id := range a.registrations {
		titles = append(titles, id)
	}
	a.mu.Unlock()
	for _, id := range titles {
		a.refreshTitle(ctx, id)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked()
	page := protocol.SessionPage{Sessions: []protocol.Session{}}
	ids := make([]string, 0, len(a.registrations))
	for id := range a.registrations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := a.registrations[id]
		access := a.accessLocked(r)
		status := r.status
		page.Sessions = append(page.Sessions, protocol.Session{ID: id,
			Title: observedTitle(r), Cwd: r.cwd,
			UpdatedAt: float64(r.updated.Unix()), Status: protocol.Known(&status), Access: &access})
	}
	return page, nil
}

func (a *Adapter) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionAccess{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err := a.findLocked(id)
	if err != nil {
		return protocol.SessionAccess{}, err
	}
	return a.accessLocked(r), nil
}

func (a *Adapter) ValidateAccess(ctx context.Context, id, binding string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err := a.findLocked(id)
	if err != nil {
		return err
	}
	if binding == "" || binding != r.nonce {
		return errors.New("Claude Mod native access binding changed")
	}
	return nil
}

func (a *Adapter) Read(ctx context.Context, id, cursor string, activity bool, excludedTurns []string) (protocol.ConversationPage, error) {
	if err := ctx.Err(); err != nil {
		return protocol.ConversationPage{}, err
	}
	if cursor != "" {
		return protocol.ConversationPage{}, errors.New("Claude history before registration or outside the bounded observation window is unsupported")
	}
	if len(excludedTurns) != 0 {
		return protocol.ConversationPage{}, errors.New("Claude observed rows have no proven native turn association")
	}
	a.refreshTitle(ctx, id)
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err := a.findLocked(id)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	access := a.accessLocked(r)
	page := protocol.ConversationPage{Messages: []protocol.Message{}, Queued: []protocol.QueuedInput{},
		Access: &access, Runtime: runtimeView(r)}
	for i := range r.inputs {
		input := &r.inputs[i]
		if input.Receipt.State == "submitted" && a.now().After(input.Deadline) {
			input.Receipt.State = "unconfirmed"
		}
		page.InputReceipts = append(page.InputReceipts, input.Receipt)
	}
	for _, row := range r.rows {
		if row.Activity != nil && !activity {
			continue
		}
		// Append rows retain native UUIDs without inferred turns. Stream blocks
		// carry their own explicit turn/step/block or tool-use identity.
		page.Messages = append(page.Messages, observedMessage(row))
	}
	return page, nil
}

func observedTitle(r *registration) protocol.Text {
	if r.title != nil {
		return *r.title
	}
	return protocol.Text("")
}

func runtimeView(r *registration) *protocol.Runtime {
	var active, completed *string
	if r.activeTurn != "" {
		value := r.activeTurn
		active = &value
	}
	if r.completedTurn != "" {
		value := r.completedTurn
		completed = &value
	}
	result := &protocol.Runtime{Status: r.status, ActiveTurnID: active,
		ActiveTurnHasUserMessage: protocol.Known(active != nil && r.activeHasUser), Title: r.title}
	if r.settingsChange != nil {
		value := *r.settingsChange
		result.SettingsChange = &value
	}
	if r.model != nil {
		value := *r.model
		result.Model = &value
	}
	if r.effort != nil {
		value := *r.effort
		result.Effort = protocol.Known(&value)
	}
	if r.contextTokens != nil {
		value := *r.contextTokens
		result.ContextTokens = &value
	}
	if r.contextWindow != nil {
		value := *r.contextWindow
		result.ContextWindow = &value
	}
	if completed != nil {
		result.CompletedTurnID = protocol.Known(completed)
	}
	return result
}

func (a *Adapter) Statuses(ctx context.Context, ids []string) ([]protocol.SessionStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ids) > maxSessions {
		return nil, errors.New("Too many Claude Mod status requests")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]protocol.SessionStatus, 0, len(ids))
	for _, id := range ids {
		r, err := a.findLocked(id)
		if err != nil {
			return nil, err
		}
		runtime := runtimeView(r)
		status := runtime.Status
		result = append(result, protocol.SessionStatus{ID: id, Status: &status,
			ActiveTurnID: runtime.ActiveTurnID, CompletedTurnID: runtime.CompletedTurnID.Value})
	}
	return result, nil
}

// Attach observes a registered Mod only. It never starts/resumes a session and
// the native turn's lifetime is independent of this no-op observer lease.
func (a *Adapter) Attach(ctx context.Context, id string) (func(), error) {
	if _, err := a.Access(ctx, id); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// Send uses the native Mod with submitted/observed, explicitly non-exact receipts.
func (a *Adapter) Send(ctx context.Context, id, text, clientID string) (protocol.SendReceipt, error) {
	return a.sendObserved(ctx, id, text, clientID)
}

func (a *Adapter) Stop(ctx context.Context, id, turnID string) error {
	return a.stopNative(ctx, id, turnID)
}

func (a *Adapter) Create(context.Context, string, string, protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	return protocol.Session{}, protocol.SendReceipt{}, errors.New("Claude supports only open opted-in Mod sessions; creation is unsupported")
}

func (a *Adapter) SetModel(ctx context.Context, id, model string) error {
	return a.updateNativeSettings(ctx, id, protocol.SessionSettings{Model: protocol.Known(model)})
}

func (a *Adapter) UpdateSettings(ctx context.Context, id string, settings protocol.SessionSettings) error {
	return a.updateNativeSettings(ctx, id, settings)
}

func (a *Adapter) Rename(context.Context, string, string) (protocol.Session, error) {
	return protocol.Session{}, unsupported("rename")
}

func (a *Adapter) Archive(context.Context, string) (string, error) {
	return "", unsupported("archive")
}

func unsupported(operation string) error {
	return errors.New("Claude " + operation + " is unsupported")
}
