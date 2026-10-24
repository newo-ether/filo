package claude

import (
	"context"
	"errors"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

// Input observations are deliberately weaker than exact native input receipts.
// Keep unknown attempts until registration loss; never dispatch them twice.
type modInput struct {
	ClientID      string
	Text          string
	CommandID     string
	AfterSequence uint64
	Receipt       protocol.InputReceipt
	Matched       bool
	Accepted      bool
	Deadline      time.Time
}

func (a *Adapter) sendObserved(ctx context.Context, id, text, clientID string) (protocol.SendReceipt, error) {
	if text == "" || len(text) > maxRowBytes || !opaque.MatchString(clientID) {
		return protocol.SendReceipt{}, errors.New("Invalid bounded Claude input")
	}
	command := modCommand{Operation: "send", Text: text, ClientID: clientID}
	pending, nonce, err := a.queueMutation(ctx, id, command)
	if err != nil {
		return protocol.SendReceipt{}, err
	}
	defer a.releaseMutation(id, nonce, pending)
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		a.mu.Lock()
		r, findErr := a.findLocked(id)
		if findErr != nil || r.nonce != nonce {
			a.mu.Unlock()
			return protocol.SendReceipt{ClientID: clientID, Kind: "submitted_observed", State: "unconfirmed"}, nil
		}
		input := findModInput(r, clientID)
		if input == nil {
			a.mu.Unlock()
			return protocol.SendReceipt{ClientID: clientID, Kind: "submitted_observed", State: "unconfirmed"}, nil
		}
		result := pending.result
		receipt := input.Receipt
		a.mu.Unlock()
		if result != nil && !result.OK {
			return protocol.SendReceipt{}, result.inputError()
		}
		if receipt.State == "observed" || receipt.State == "submitted" {
			return protocol.SendReceipt{ClientID: clientID, TurnID: receipt.TurnID, MessageID: receipt.MessageID,
				Kind: receipt.Kind, State: receipt.State}, nil
		}
		select {
		case <-ctx.Done():
			return a.unknownInput(id, nonce, clientID), nil
		case <-timer.C:
			return a.unknownInput(id, nonce, clientID), nil
		case <-pending.changed:
		}
	}
}

func findModInput(r *registration, clientID string) *modInput {
	for i := range r.inputs {
		if r.inputs[i].ClientID == clientID {
			return &r.inputs[i]
		}
	}
	return nil
}

func (a *Adapter) unknownInput(id, nonce, clientID string) protocol.SendReceipt {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r := a.registrations[id]; r != nil && r.nonce == nonce {
		if input := findModInput(r, clientID); input != nil {
			if input.Receipt.State == "observed" {
				return protocol.SendReceipt{ClientID: clientID, Kind: "submitted_observed", State: "observed", TurnID: input.Receipt.TurnID, MessageID: input.Receipt.MessageID}
			}
			input.Receipt.State = "unconfirmed"
		}
	}
	return protocol.SendReceipt{ClientID: clientID, Kind: "submitted_observed", State: "unconfirmed"}
}

func (a *Adapter) queueMutation(ctx context.Context, id string, command modCommand) (*pendingModCommand, string, error) {
	if err := protocol.ValidateScope(ctx); err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	commandID, err := randomNonce()
	if err != nil {
		return nil, "", err
	}
	command.ID = commandID
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err := a.findLocked(id)
	if err != nil {
		return nil, "", err
	}
	if protocol.AccessBinding(ctx) == "" || protocol.AccessBinding(ctx) != r.nonce {
		return nil, "", errors.New("Claude Mod native access binding changed")
	}
	if r.pending != nil {
		return nil, "", errors.New("Claude native control is busy")
	}
	if command.Operation == "send" {
		// Native prompt.submit waits for idle when no exact active turn is known.
		// Keep runtime unknown until native lifecycle evidence arrives.
		if !r.canInput || (r.status != "idle" && r.status != "active" && r.status != "unknown") {
			return nil, "", errors.New("Claude native input unavailable")
		}
		for _, input := range r.inputs {
			if input.ClientID == command.ClientID || input.Receipt.State != "observed" && input.Receipt.State != "rejected" {
				return nil, "", errors.New("Claude previous input is unconfirmed; resend blocked")
			}
		}
		command.TurnID = r.activeTurn
		r.inputs = append(r.inputs, modInput{ClientID: command.ClientID, Text: command.Text, CommandID: commandID,
			AfterSequence: r.sequence, Deadline: a.now().Add(20 * time.Second),
			Receipt: protocol.InputReceipt{ClientID: command.ClientID, Kind: "submitted_observed", State: "unconfirmed"}})
		if len(r.inputs) > maxRows {
			r.inputs = append([]modInput(nil), r.inputs[len(r.inputs)-maxRows:]...)
		}
	} else if !r.canSettings {
		return nil, "", errors.New("Claude native settings unavailable")
	}
	pending := &pendingModCommand{command: command, ctx: ctx, changed: make(chan struct{}, 1)}
	if command.Operation == "settings" {
		r.settingsAfterTurn = r.activeTurn
		r.settingsChange = &protocol.SettingsChange{ID: commandID, State: "pending", Requested: *command.Settings}
	}
	r.pending = pending
	return pending, r.nonce, nil
}

func (a *Adapter) releaseMutation(id, nonce string, pending *pendingModCommand) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r := a.registrations[id]; r != nil && r.nonce == nonce && r.pending == pending {
		r.pending = nil
	}
}

func (a *Adapter) updateNativeSettings(ctx context.Context, id string, settings protocol.SessionSettings) error {
	if settings.ServiceTier.Known || !settings.Model.Known && !settings.Effort.Known {
		return unsupported("settings")
	}
	if settings.Effort.Known && !namedEffort(settings.Effort.Value) {
		return errors.New("Invalid Claude effort")
	}
	if settings.Model.Known {
		a.mu.Lock()
		r, err := a.findLocked(id)
		found := false
		if err == nil {
			for _, model := range r.modelOptions {
				found = found || model == settings.Model.Value
			}
		}
		a.mu.Unlock()
		if !found {
			return errors.New("Claude model is outside native catalog")
		}
	}
	pending, nonce, err := a.queueMutation(ctx, id, modCommand{Operation: "settings", Settings: &settings})
	if err != nil {
		return err
	}
	defer a.releaseMutation(id, nonce, pending)
	a.changed(id)
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		a.mu.Lock()
		r, findErr := a.findLocked(id)
		if findErr != nil || r.nonce != nonce {
			a.mu.Unlock()
			return errors.New("Claude settings outcome unconfirmed")
		}
		result := pending.result
		a.mu.Unlock()
		if result != nil {
			if !result.OK {
				return errors.New("Claude native settings refused")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("Claude settings submission unconfirmed")
		case <-timer.C:
			return errors.New("Claude settings submission unconfirmed")
		case <-pending.changed:
		}
	}
}

func namedEffort(value string) bool {
	return value == "low" || value == "medium" || value == "high" || value == "xhigh" || value == "max"
}
