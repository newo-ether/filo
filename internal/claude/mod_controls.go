package claude

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

// Commands are delivered once. A lost response never permits a native replay.
type modCommand struct {
	HostSessionID string                    `json:"hostSessionId,omitempty"`
	ID            string                    `json:"id"`
	Operation     string                    `json:"operation"`
	Title         string                    `json:"title,omitempty"`
	TurnID        string                    `json:"turnId,omitempty"`
	Text          string                    `json:"text,omitempty"`
	ClientID      string                    `json:"clientId,omitempty"`
	Settings      *protocol.SessionSettings `json:"settings,omitempty"`
}

type modCommandResult struct {
	Rename   *desktopMetadata `json:"rename,omitempty"`
	Metadata *desktopMetadata `json:"metadata,omitempty"`
	ID       string           `json:"id"`
	OK       bool             `json:"ok"`
	Refusal  string           `json:"refusal,omitempty"`
}

func (r modCommandResult) inputError() error {
	switch r.Refusal {
	case "append_read_only":
		return errors.New("Claude native append refused: main is a conversation no plugin may shape")
	case "append_unbound":
		return errors.New("Claude native append refused: this session keeps no conversation to append to")
	case "append_identity":
		return errors.New("Claude native append refused: a uuid the engine did not mint")
	case "append_shape":
		return errors.New("Claude native append refused the user-note shape")
	default:
		return errors.New("Claude native input refused")
	}
}

type pendingModCommand struct {
	desktopIdentity *desktopIdentity
	command         modCommand
	ctx             context.Context
	delivered       bool
	completed       bool
	result          *modCommandResult
	changed         chan struct{}
}

func (a *Adapter) stopNative(ctx context.Context, id, turnID string) error {
	if err := protocol.ValidateScope(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	commandID, err := randomNonce()
	if err != nil {
		return err
	}
	a.mu.Lock()
	r, err := a.findLocked(id)
	if err == nil && (protocol.AccessBinding(ctx) == "" || protocol.AccessBinding(ctx) != r.nonce) {
		err = errors.New("Claude Mod native access binding changed")
	}
	if err == nil && (!r.canStop || r.status != "active" || turnID == "" || r.activeTurn != turnID) {
		err = errors.New("Claude native active turn or abort capability changed")
	}
	if err == nil && r.pending != nil {
		err = errors.New("Claude native control is busy")
	}
	if err != nil {
		a.mu.Unlock()
		return err
	}
	pending := &pendingModCommand{command: modCommand{ID: commandID, Operation: "stop", TurnID: turnID},
		ctx: ctx, changed: make(chan struct{}, 1)}
	r.pending = pending
	nonce := r.nonce
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if current := a.registrations[id]; current != nil && current.nonce == nonce && current.pending == pending {
			current.pending = nil
		}
		a.mu.Unlock()
	}()
	for {
		a.mu.Lock()
		current, findErr := a.findLocked(id)
		if findErr != nil || current.nonce != nonce || current.pending != pending {
			a.mu.Unlock()
			return errors.New("Claude Mod registration changed during native control; outcome unknown")
		}
		result, completed := pending.result, pending.completed
		a.mu.Unlock()
		if result != nil {
			if !result.OK {
				return errors.New("Claude native abort refused; input was not replayed")
			}
			if completed {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("Claude native abort outcome unconfirmed; input was not replayed")
		case <-pending.changed:
		}
	}
}

func notifyModCommand(r *registration) {
	if r != nil && r.pending != nil {
		select {
		case r.pending.changed <- struct{}{}:
		default:
		}
	}
}

// Validate the public source scope outside the adapter lock, then check the
// native registration again immediately before the sole command delivery.
func (a *Adapter) pull(w http.ResponseWriter, input envelope) {
	if len(input.Events) != 0 || input.Result != nil || input.Sequence != 0 {
		a.respond(w, http.StatusConflict, bridgeResponse{Error: "Pull cannot include events or results"})
		return
	}
	a.mu.Lock()
	r, err := a.findLocked(input.SessionID)
	if err != nil || r.nonce != input.Nonce {
		a.mu.Unlock()
		a.respond(w, http.StatusConflict, bridgeResponse{Error: "Claude Mod registration changed"})
		return
	}
	pending := r.pending
	a.mu.Unlock()
	if pending != nil {
		err = protocol.ValidateScope(pending.ctx)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current, findErr := a.findLocked(input.SessionID)
	if findErr != nil || current != r || current.nonce != input.Nonce {
		a.respond(w, http.StatusConflict, bridgeResponse{Error: "Claude Mod registration changed"})
		return
	}
	result := bridgeResponse{ReadOnly: !r.canStop, TTLMillis: a.ttl.Milliseconds(), Sequence: r.sequence,
		HistoryBoundary: HistoryBoundary, Commands: []any{}}
	r.expires = a.now().Add(a.ttl)
	if pending != nil && r.pending == pending && !pending.delivered && err == nil &&
		pending.ctx.Err() == nil && ((pending.command.Operation == "stop" && r.canStop && r.status == "active" && r.activeTurn == pending.command.TurnID) ||
		(pending.command.Operation == "send" && r.canInput && r.activeTurn == pending.command.TurnID &&
			(r.status == "active" || r.status == "idle" || r.status == "unknown")) ||
		(pending.command.Operation == "settings" && r.canSettings) ||
		((pending.command.Operation == "desktop-metadata" || pending.command.Operation == "desktop-rename") &&
			r.origin == "desktop" && pending.desktopIdentity.unchanged())) {
		pending.delivered = true
		result.Commands = append(result.Commands, pending.command)
	}
	a.respond(w, http.StatusOK, result)
}

func (a *Adapter) commandResultLocked(r *registration, input envelope) error {
	if input.Result == nil || len(input.Events) != 0 || input.Sequence != 0 || r.pending == nil ||
		!r.pending.delivered || r.pending.command.ID != input.Result.ID {
		return errors.New("Claude native command result does not match dispatched control")
	}
	if err := validateMetadataResult(r, input.Result); err != nil {
		return err
	}
	if r.pending.result != nil && !sameModCommandResult(r.pending.result, input.Result) {
		return errors.New("Claude native command result changed")
	}
	result := *input.Result
	if result.Metadata != nil {
		metadata := *result.Metadata
		result.Metadata = &metadata
	}
	if result.Rename != nil {
		rename := *result.Rename
		result.Rename = &rename
	}
	r.pending.result = &result
	if r.pending.command.Operation == "send" {
		if input := findModInput(r, r.pending.command.ClientID); input != nil {
			input.Accepted = result.OK
			if !result.OK {
				input.Receipt.State = "rejected"
			} else if input.Matched {
				input.Receipt.State = "observed"
			} else if input.Receipt.State != "observed" {
				input.Receipt.State = "submitted"
			}
		}
	}
	if r.pending.command.Operation == "settings" && !result.OK && r.settingsChange != nil {
		r.settingsChange.State = "failed"
	}
	notifyModCommand(r)
	return nil
}

func (a *Adapter) registrationCapabilities(r *registration) protocol.AdapterCapabilities {
	caps := Capabilities()
	if a.origin == "desktop" && r.origin == "desktop" {
		caps.Rename = resolveDesktopIdentity(context.Background(), r.nativeID) != nil
	}
	caps.Stop = r.canStop && r.status == "active" && r.activeTurn != ""
	caps.IdleSend = r.canInput
	if r.canInput {
		caps.ActiveSend = "native-steer"
	} else {
		caps.ActiveSend = "unsupported"
	}
	if !r.canSettings {
		caps.Settings = []string{}
	}
	return caps
}
