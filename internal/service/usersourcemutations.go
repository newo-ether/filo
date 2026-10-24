package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/newo-ether/filo/internal/protocol"
)

func (source *userSource) Create(ctx context.Context, text, clientID string,
	settings protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	scope := protocol.NativeScopeID(ctx)
	if clientID == "" || protocol.AccessBinding(ctx) != "" {
		return protocol.Session{}, protocol.SendReceipt{}, errors.New("Invalid private source creation")
	}
	payload := map[string]any{"text": text, "clientId": clientID, "nativeScopeId": scope}
	if object := createSettingsObject(settings); object != nil {
		payload["settings"] = object
	}
	if err := source.checkScope(ctx, scope); err != nil {
		return protocol.Session{}, protocol.SendReceipt{}, err
	}
	raw, err := source.worker.call(ctx, source.path("sessions"), payload)
	if err != nil {
		return protocol.Session{}, protocol.SendReceipt{}, err
	}
	var value createSessionResponse
	if err := json.Unmarshal(raw, &value); err != nil {
		return protocol.Session{}, protocol.SendReceipt{}, err
	}
	if !validSessionRef(value.ID) || value.TurnID == "" || value.ClientID != clientID {
		return protocol.Session{}, protocol.SendReceipt{}, errors.New("Unconfirmed private source creation")
	}
	if err := source.checkScope(ctx, scope); err != nil {
		return protocol.Session{}, protocol.SendReceipt{}, err
	}
	source.remember(value.ID, value.Access)
	return value.Session, protocol.SendReceipt{TurnID: value.TurnID, ClientID: value.ClientID}, nil
}

// callMutation keeps the private worker's sealed binding intact. The public
// layer has already removed its own seal; neither transport selects another owner.
func (source *userSource) callMutation(ctx context.Context, id, action string,
	payload map[string]any) (json.RawMessage, error) {
	binding := protocol.AccessBinding(ctx)
	if err := source.ValidateAccess(ctx, id, binding); err != nil {
		return nil, err
	}
	scope := protocol.NativeScopeID(ctx)
	// The scoped private route derives identity from its sealed binding. Create
	// alone takes nativeScopeId; settings accepts only actual setting fields.
	payload["accessBinding"] = binding
	if err := source.checkScope(ctx, scope); err != nil {
		return nil, err
	}
	return source.worker.call(ctx, source.path("sessions/"+url.PathEscape(id)+"/"+action), payload)
}

func (source *userSource) Send(ctx context.Context, id, text, clientID string) (protocol.SendReceipt, error) {
	if clientID == "" {
		return protocol.SendReceipt{}, errors.New("Invalid private source client identity")
	}
	raw, err := source.callMutation(ctx, id, "messages", map[string]any{"text": text, "clientId": clientID})
	if err != nil {
		return protocol.SendReceipt{}, err
	}
	var receipt protocol.SendReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return protocol.SendReceipt{}, err
	}
	valid := receipt.Kind == "" && receipt.State == "" && receipt.TurnID != ""
	if source.id == "claude-code" || source.id == "claude-desktop" {
		valid = valid || receipt.Kind == "submitted_observed" &&
			(receipt.State == "submitted" || receipt.State == "unconfirmed" ||
				receipt.State == "observed" && (receipt.TurnID != "" || receipt.MessageID != ""))
	}
	if !valid || receipt.ClientID != clientID || len(receipt.TurnID) > 1024 || len(receipt.MessageID) > 1024 {
		return protocol.SendReceipt{}, errors.New("Unconfirmed private source input")
	}
	return receipt, nil
}

func (source *userSource) SetModel(ctx context.Context, id, model string) error {
	return source.confirmMutation(ctx, id, "model", map[string]any{"model": model}, "updated")
}

func (source *userSource) UpdateSettings(ctx context.Context, id string, settings protocol.SessionSettings) error {
	payload := createSettingsObject(settings)
	if payload == nil {
		payload = map[string]any{}
	}
	return source.confirmMutation(ctx, id, "settings", payload, "updated")
}

func (source *userSource) Stop(ctx context.Context, id, turnID string) error {
	return source.confirmMutation(ctx, id, "stop", map[string]any{"turnId": turnID}, "stopped")
}

func (source *userSource) confirmMutation(ctx context.Context, id, action string,
	payload map[string]any, confirmation string) error {
	raw, err := source.callMutation(ctx, id, action, payload)
	if err != nil {
		return err
	}
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return err
	}
	var confirmed bool
	if json.Unmarshal(receipt[confirmation], &confirmed) != nil || !confirmed {
		return errors.New("Unconfirmed private source mutation")
	}
	return nil
}

func (source *userSource) Rename(ctx context.Context, id, name string) (protocol.Session, error) {
	raw, err := source.callMutation(ctx, id, "rename", map[string]any{"name": name})
	if err != nil {
		return protocol.Session{}, err
	}
	var session protocol.Session
	if err := json.Unmarshal(raw, &session); err != nil {
		return protocol.Session{}, err
	}
	if session.ID != id {
		return protocol.Session{}, errors.New("Unconfirmed private source rename")
	}
	return session, nil
}

func (source *userSource) Archive(ctx context.Context, id string) (string, error) {
	raw, err := source.callMutation(ctx, id, "archive", map[string]any{})
	if err != nil {
		return "", err
	}
	var receipt struct {
		Archived bool    `json:"archived"`
		Cwd      *string `json:"cwd"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return "", err
	}
	if !receipt.Archived || receipt.Cwd == nil {
		return "", errors.New("Unconfirmed private source archive")
	}
	return *receipt.Cwd, nil
}
