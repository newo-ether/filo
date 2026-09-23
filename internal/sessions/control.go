package sessions

import (
	"context"
	"errors"
	"math"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

const (
	// nativeOperationUnconfirmed is the TS fallback of a native operation whose
	// outcome the original owner did not confirm.
	nativeOperationUnconfirmed = "Native desktop operation outcome is unconfirmed"
	nativeSteerUnknown         = "Unknown native steer outcome"
	nativeStartUnknown         = "Unknown native start outcome"
	nativeStopUnconfirmed      = "Native stop was not fully confirmed"
	nativeSettingsUnconfirmed  = "Native settings outcome is unconfirmed"
)

var (
	// errSessionCatalog guards the peripheral catalog of a surface that was built
	// without one. The TS base object is always present, so this is defensive.
	errSessionCatalog = errors.New("Filo session catalog is unavailable")
	// errCreatedScope guards the created-task scope in the same position where the
	// TS adapter asserts `base.newTasks!`.
	errCreatedScope = errors.New("Filo created-task scope is unavailable")
)

// Create starts one native task through the peripheral catalog and immediately
// runs its first turn. The desktop surface forwards the operation and never
// admits a writer of its own.
func (sessions *DesktopSessions) Create(ctx context.Context, text, clientID string,
	settings protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	if sessions.options.Base == nil {
		return protocol.Session{}, protocol.SendReceipt{}, errSessionCatalog
	}
	return sessions.options.Base.Create(ctx, text, clientID, settings)
}

// Models reads the native model catalog.
func (sessions *DesktopSessions) Models(ctx context.Context) ([]protocol.Model, error) {
	if sessions.options.Base == nil {
		return nil, errSessionCatalog
	}
	return sessions.options.Base.Models(ctx)
}

// Rename changes the native task name.
func (sessions *DesktopSessions) Rename(ctx context.Context, id, name string) (protocol.Session, error) {
	if sessions.options.Base == nil {
		return protocol.Session{}, errSessionCatalog
	}
	return sessions.options.Base.Rename(ctx, id, name)
}

// Archive ends one task natively and publishes the archived thread only after
// that native call succeeded. Archive never checks ownership and never sends a
// stop: the native catalog is the only authority on archiving.
func (sessions *DesktopSessions) Archive(ctx context.Context, id string) (string, error) {
	if sessions.f.isStopped() {
		return "", unavailable()
	}
	if err := sessions.options.IPC.Connect(ctx); err != nil {
		return "", err
	}
	if sessions.f.isStopped() {
		return "", unavailable()
	}
	if sessions.options.Base == nil {
		return "", errSessionCatalog
	}
	cwd, err := sessions.options.Base.Archive(ctx, id)
	if err != nil {
		return "", err
	}
	if err := sessions.options.IPC.Broadcast("thread-archived", 2,
		map[string]any{"hostId": "local", "conversationId": id, "cwd": cwd}, nil); err != nil {
		return "", err
	}
	sessions.f.forget(id)
	return cwd, nil
}

// SetModel is the single-key settings mutation the TS adapter exposes on top of
// updateSettings.
func (sessions *DesktopSessions) SetModel(ctx context.Context, id, model string) error {
	return sessions.UpdateSettings(ctx, id, protocol.SessionSettings{Model: protocol.Known(model)})
}

// UpdateSettings validates the requested capabilities against the confirmed
// snapshot and mutates the original owner. A confirmed acknowledgement is not a
// readback: the next session snapshot still reports the native truth.
func (sessions *DesktopSessions) UpdateSettings(ctx context.Context, id string, settings protocol.SessionSettings) error {
	owner, err := sessions.destination(ctx, id)
	if err != nil {
		return err
	}
	if owner == "" {
		if sessions.options.Created == nil {
			return errCreatedScope
		}
		return sessions.options.Created.UpdateSettings(ctx, id, settings)
	}
	state, err := sessions.controlState(ctx, id, owner)
	if err != nil {
		return err
	}
	models, err := sessions.Models(ctx)
	if err != nil {
		return err
	}
	var current *string
	if latest := text(state["latestModel"]); latest != "" {
		current = &latest
	}
	if err := codex.ValidateSettings(settings, models, current); err != nil {
		return err
	}
	result, err := sessions.operation(ctx, id, owner, "thread-follower-update-thread-settings", 1,
		map[string]any{"threadSettings": settings})
	if err != nil {
		return err
	}
	if ok, isBool := fields(result)["ok"].(bool); !isBool || !ok {
		return errors.New(nativeSettingsUnconfirmed)
	}
	sessions.f.changed(id)
	return nil
}

// Send delivers one input to the original owner: an active turn is steered, an
// idle task starts exactly one new turn, and anything else is refused instead of
// falling back to another execution path.
func (sessions *DesktopSessions) Send(ctx context.Context, id, value, clientID string) (protocol.SendReceipt, error) {
	var empty protocol.SendReceipt
	owner, err := sessions.destination(ctx, id)
	if err != nil {
		return empty, err
	}
	if owner == "" {
		if sessions.options.Created == nil {
			return empty, errCreatedScope
		}
		return sessions.options.Created.Send(ctx, id, value, clientID)
	}
	state, err := sessions.controlState(ctx, id, owner)
	if err != nil {
		return empty, err
	}
	page, err := codex.ProjectDesktop(state, false, 128)
	if err != nil {
		return empty, err
	}
	input := desktopInput(value)
	if page.Runtime != nil && page.Runtime.Status == "active" && page.Runtime.ActiveTurnID != nil &&
		*page.Runtime.ActiveTurnID != "" {
		result, err := sessions.operation(ctx, id, owner, "thread-follower-steer-turn", 1, map[string]any{
			"input":               input,
			"clientUserMessageId": clientID,
			"attachments":         []any{},
			"restoreMessage": map[string]any{
				"id":   clientID,
				"text": value,
				"cwd":  state["cwd"],
				"context": map[string]any{
					"commentAttachments": []any{},
					"workspaceRoots":     []any{state["cwd"]},
				},
			},
		})
		if err != nil {
			return empty, err
		}
		turnID, ok := nativejson.AsText(fields(fields(result)["result"])["turnId"])
		if !ok {
			return empty, errors.New(nativeSteerUnknown)
		}
		return protocol.SendReceipt{TurnID: turnID, ClientID: clientID}, nil
	}
	if page.Runtime == nil || page.Runtime.Status != "idle" {
		return empty, unavailable()
	}
	result, err := sessions.operation(ctx, id, owner, "thread-follower-start-turn", 2, map[string]any{
		"turnStart": map[string]any{
			"request": map[string]any{"threadId": id, "input": input, "clientUserMessageId": clientID},
			"context": map[string]any{"inheritThreadSettings": true},
		},
	})
	if err != nil {
		return empty, err
	}
	turn := fields(fields(fields(result)["result"])["turn"])
	turnID, ok := nativejson.AsText(turn["id"])
	if !ok {
		return empty, errors.New(nativeStartUnknown)
	}
	return protocol.SendReceipt{TurnID: turnID, ClientID: clientID}, nil
}

// Stop interrupts exactly the confirmed active turn of the original owner. A
// stale turn id, or a native outcome that leaves the turn running, is refused.
func (sessions *DesktopSessions) Stop(ctx context.Context, id, turnID string) error {
	owner, err := sessions.destination(ctx, id)
	if err != nil {
		return err
	}
	if owner == "" {
		if sessions.options.Created == nil {
			return errCreatedScope
		}
		return sessions.options.Created.Stop(ctx, id, turnID)
	}
	state, err := sessions.controlState(ctx, id, owner)
	if err != nil {
		return err
	}
	page, err := codex.ProjectDesktop(state, false, 128)
	if err != nil {
		return err
	}
	if page.Runtime == nil || page.Runtime.ActiveTurnID == nil || *page.Runtime.ActiveTurnID != turnID {
		return unavailable()
	}
	result, err := sessions.operation(ctx, id, owner, "thread-follower-interrupt-turn", 4,
		map[string]any{"mode": "user-stop", "expectedTurnId": turnID})
	if err != nil {
		return err
	}
	outcome := fields(result)
	if text(outcome["interruptedTurnId"]) != turnID || jsTruthy(outcome["goalPauseError"]) {
		return errors.New(nativeStopUnconfirmed)
	}
	return nil
}

// controlState loads the confirmed snapshot a control mutation must be based on.
func (sessions *DesktopSessions) controlState(ctx context.Context, id, owner string) (codex.DesktopState, error) {
	entry, err := sessions.f.state(ctx, id, owner, false, false)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, unavailable()
	}
	return sessions.f.snapshot(entry)
}

// destination resolves where one mutation may go. A task the original desktop
// no longer owns is only reachable through the separately verified created
// scope, and the owner is rechecked so an owner appearing mid-dispatch refuses
// the auxiliary path.
func (sessions *DesktopSessions) destination(ctx context.Context, id string) (string, error) {
	owner, err := sessions.f.owner(ctx, id)
	if err != nil {
		return "", err
	}
	if owner != "" {
		return owner, nil
	}
	created, err := sessions.created(ctx, id)
	if err != nil {
		return "", err
	}
	if !created {
		return "", unavailable()
	}
	confirmed, err := sessions.f.owner(ctx, id)
	if err != nil {
		return "", err
	}
	if confirmed != "" {
		return "", unavailable()
	}
	return "", nil
}

// operation rechecks the exact confirmed owner and refuses to report an
// unconfirmed native outcome as a success.
func (sessions *DesktopSessions) operation(ctx context.Context, id, owner, method string, version int, params map[string]any) (any, error) {
	current, err := sessions.f.owner(ctx, id)
	if err != nil {
		return nil, err
	}
	if current != owner {
		return nil, unavailable()
	}
	merged := make(map[string]any, len(params)+1)
	merged["conversationId"] = id
	for key, value := range params {
		merged[key] = value
	}
	response, err := sessions.options.IPC.Request(ctx, method, version, merged, owner)
	if err != nil {
		return nil, err
	}
	if text(response["resultType"]) != "success" || text(response["handledByClientId"]) != owner {
		message := text(response["error"])
		if message == "" {
			message = nativeOperationUnconfirmed
		}
		return nil, errors.New(message)
	}
	return response["result"], nil
}

// desktopInput builds the single text part every native turn request carries.
func desktopInput(value string) []any {
	return []any{map[string]any{"type": "text", "text": value, "text_elements": []any{}}}
}

// jsTruthy reproduces the JS truthiness of a native field, which the TS
// `result?.goalPauseError` check relies on.
func jsTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case float64:
		return typed != 0 && !math.IsNaN(typed)
	case string:
		return typed != ""
	default:
		return true
	}
}
