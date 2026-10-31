package claude

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/newo-ether/filo/internal/protocol"
)

// Private qualification uses the same fixed command as public Rename.
// This diagnostic request leaves public title/history projection untouched.
type desktopRenameRequest struct {
	envelope
	Title string `json:"title"`
}

func validDesktopRenameTitle(title string) bool {
	return utf8.ValidString(title) && strings.TrimSpace(title) != "" &&
		title == strings.TrimSpace(title) && len(utf16.Encode([]rune(title))) <= 200
}

func (a *Adapter) queryDesktopRename(w http.ResponseWriter, request *http.Request) {
	var input desktopRenameRequest
	if decodeRequest(request, &input) != nil || !nativeUUID.MatchString(input.SessionID) ||
		!opaque.MatchString(input.Nonce) || !validDesktopRenameTitle(input.Title) {
		a.respond(w, http.StatusBadRequest, bridgeResponse{Error: "Invalid Desktop rename request"})
		return
	}
	a.queryDesktopDiagnostic(w, request, input.envelope, modCommand{Operation: "desktop-rename", Title: input.Title})
}

// Rename publishes only a fresh exact native applied readback. Permission hooks
// and dialogs remain owned by the native tool; uncertain outcomes are not replayed.
func (a *Adapter) Rename(ctx context.Context, id, title string) (protocol.Session, error) {
	if a.origin != "desktop" {
		return protocol.Session{}, unsupported("rename")
	}
	if !validDesktopRenameTitle(title) {
		return protocol.Session{}, errors.New("Invalid Desktop rename title")
	}
	value, session, err := a.executeDesktopDiagnostic(ctx, id, protocol.AccessBinding(ctx),
		modCommand{Operation: "desktop-rename", Title: title}, true)
	if err != nil {
		return protocol.Session{}, err
	}
	if value.Status != "applied" {
		if value.Status == "refused" {
			return protocol.Session{}, errors.New("Claude native rename refused")
		}
		return protocol.Session{}, errors.New("Claude native rename outcome unknown; request was not replayed")
	}
	a.changed(id)
	return session, nil
}

func validateRenameResult(r *registration, result *modCommandResult) error {
	if result.Rename == nil || result.Metadata != nil || result.Refusal != "" {
		return errors.New("Desktop rename readback required")
	}
	rename := result.Rename
	if rename.Status == "applied" {
		if !result.OK || rename.Reason != "" || !validDesktopReadbackIdentity(r, rename) ||
			rename.Title == nil || *rename.Title != r.pending.command.Title {
			return errors.New("Desktop rename native session or title changed")
		}
		return nil
	}
	switch rename.Status {
	case "refused", "invalid", "unknown", "busy":
		if !result.OK && rename.SessionID == "" && rename.HostSessionID == "" && rename.Title == nil && validDesktopReason(rename) {
			return nil
		}
	}
	return errors.New("Invalid Desktop rename result")
}
