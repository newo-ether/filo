package claude

import (
	"context"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/newo-ether/filo/internal/protocol"
)

// A matched readback qualifies only this operation, not every MCP route.
type desktopMetadata struct {
	Status        string  `json:"status"`
	Reason        string  `json:"reason,omitempty"`
	Title         *string `json:"title,omitempty"`
	SessionID     string  `json:"sessionId,omitempty"`
	HostSessionID string  `json:"hostSessionId,omitempty"`
}

func (a *Adapter) queryDesktopMetadata(w http.ResponseWriter, request *http.Request, input envelope) {
	a.queryDesktopDiagnostic(w, request, input, modCommand{Operation: "desktop-metadata"})
}

func (a *Adapter) queryDesktopDiagnostic(w http.ResponseWriter, request *http.Request, input envelope, command modCommand) {
	if a.origin != "desktop" || input.Sequence != 0 || input.Events != nil || input.Result != nil {
		a.respond(w, http.StatusConflict, bridgeResponse{Error: "Desktop diagnostic requires an explicit registration-bound envelope"})
		return
	}
	value, _, err := a.executeDesktopDiagnostic(request.Context(), input.SessionID, input.Nonce, command, false)
	if err != nil {
		a.respond(w, http.StatusConflict, bridgeResponse{Error: err.Error()})
		return
	}
	a.respond(w, http.StatusOK, desktopDiagnosticResponse(command, value))
}

// Private diagnostics and public Rename share one queue, waiter and identity fence.
// Only the public caller publishes an exact applied readback to the projection.
func (a *Adapter) executeDesktopDiagnostic(ctx context.Context, sessionID, nonce string, command modCommand, publish bool) (*desktopMetadata, protocol.Session, error) {
	var session protocol.Session
	if err := protocol.ValidateScope(ctx); err != nil {
		return nil, session, err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	id, err := randomNonce()
	if err != nil {
		return nil, session, err
	}
	a.mu.Lock()
	r, err := a.findLocked(sessionID)
	if err != nil || a.origin != "desktop" || r.origin != "desktop" || nonce == "" || r.nonce != nonce || r.pending != nil {
		a.mu.Unlock()
		return nil, session, errors.New("Claude Mod registration or control changed")
	}
	a.mu.Unlock()
	identity := resolveDesktopIdentity(ctx, r.nativeID)
	if err := protocol.ValidateScope(ctx); err != nil {
		return nil, session, err
	}
	a.mu.Lock()
	current, findErr := a.findLocked(sessionID)
	if findErr != nil || current != r || current.nonce != nonce || current.pending != nil {
		a.mu.Unlock()
		return nil, session, errors.New("Claude Mod registration or control changed")
	}
	if identity == nil || ctx.Err() != nil {
		a.mu.Unlock()
		return &desktopMetadata{Status: "invalid", Reason: "mapping-unavailable"}, session, nil
	}
	command.ID, command.HostSessionID = id, identity.hostID
	pending := &pendingModCommand{command: command, desktopIdentity: identity, ctx: ctx, changed: make(chan struct{}, 1)}
	r.pending = pending
	a.mu.Unlock()
	defer a.releaseMutation(sessionID, nonce, pending)
	for {
		if err := protocol.ValidateScope(ctx); err != nil && ctx.Err() == nil {
			return nil, session, err
		}
		a.mu.Lock()
		current, findErr := a.findLocked(sessionID)
		if findErr != nil || current != r || current.nonce != nonce || current.pending != pending {
			a.mu.Unlock()
			return nil, session, errors.New("Claude Mod registration changed; diagnostic outcome unknown")
		}
		var metadata *desktopMetadata
		if pending.result != nil {
			value := pending.result.Metadata
			if command.Operation == "desktop-rename" {
				value = pending.result.Rename
			}
			if value != nil {
				copy := *value
				metadata = &copy
			}
		}
		if metadata != nil && ctx.Err() == nil {
			a.mu.Unlock()
			if err := protocol.ValidateScope(ctx); err != nil {
				return &desktopMetadata{Status: "unknown"}, session, nil
			}
			a.mu.Lock()
			current, findErr = a.findLocked(sessionID)
			if findErr != nil || current != r || current.nonce != nonce || current.pending != pending || ctx.Err() != nil {
				a.mu.Unlock()
				return &desktopMetadata{Status: "unknown"}, session, nil
			}
			if !identity.unchanged() {
				metadata = &desktopMetadata{Status: "unknown", Reason: "mapping-changed"}
			}
			if publish && command.Operation == "desktop-rename" && metadata.Status == "applied" {
				title := protocol.Text(*metadata.Title)
				r.title, r.renameReadback, r.updated = &title, &title, a.now()
				access := a.accessLocked(r)
				session = protocol.Session{ID: sessionID, Title: title, Cwd: r.cwd,
					UpdatedAt: float64(r.updated.Unix()), Status: protocol.Known(&r.status), Access: &access}
			}
			a.mu.Unlock()
			return metadata, session, nil
		}
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return &desktopMetadata{Status: "unknown"}, session, nil
		case <-pending.changed:
		}
	}
}

func desktopDiagnosticResponse(command modCommand, value *desktopMetadata) bridgeResponse {
	if command.Operation == "desktop-rename" {
		return bridgeResponse{Rename: value, HistoryBoundary: HistoryBoundary}
	}
	return bridgeResponse{Metadata: value}
}

func validateMetadataResult(r *registration, result *modCommandResult) error {
	if r.pending.command.Operation == "desktop-rename" {
		return validateRenameResult(r, result)
	}
	if result.Rename != nil {
		return errors.New("Rename result requires its exact diagnostic command")
	}
	if r.pending.command.Operation != "desktop-metadata" {
		if result.Metadata != nil {
			return errors.New("Metadata result requires its exact diagnostic command")
		}
		return nil
	}
	if result.Metadata == nil || result.Refusal != "" {
		return errors.New("Desktop metadata result required")
	}
	metadata := result.Metadata
	if metadata.Status == "matched" {
		if !result.OK || metadata.Reason != "" || !validDesktopReadbackIdentity(r, metadata) ||
			metadata.Title != nil && (len(*metadata.Title) > 4096 || !utf8.ValidString(*metadata.Title)) {
			return errors.New("Desktop metadata native session identity changed")
		}
		return nil
	}
	switch metadata.Status {
	case "refused", "invalid", "timeout", "unavailable", "busy":
		if !result.OK && metadata.SessionID == "" && metadata.HostSessionID == "" && metadata.Title == nil && validDesktopReason(metadata) {
			return nil
		}
	}
	return errors.New("Invalid Desktop metadata result")
}

func sameModCommandResult(left, right *modCommandResult) bool {
	if left.ID != right.ID || left.OK != right.OK || left.Refusal != right.Refusal {
		return false
	}
	return sameDesktopReadback(left.Metadata, right.Metadata) && sameDesktopReadback(left.Rename, right.Rename)
}

func sameDesktopReadback(lm, rm *desktopMetadata) bool {
	if lm == nil || rm == nil {
		return lm == rm
	}
	return lm.Status == rm.Status && lm.Reason == rm.Reason && lm.SessionID == rm.SessionID && lm.HostSessionID == rm.HostSessionID &&
		(lm.Title == nil && rm.Title == nil || lm.Title != nil && rm.Title != nil && *lm.Title == *rm.Title)
}
