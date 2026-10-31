package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func startRenameHTTP(t *testing.T, a *Adapter, nonce, title string) (<-chan *httptest.ResponseRecorder, context.CancelFunc) {
	t.Helper()
	desktopIdentityFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	body, err := json.Marshal(desktopRenameRequest{envelope: envelope{SessionID: fixtureSession, Nonce: nonce}, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, bridgePrefix+"rename", strings.NewReader(string(body))).WithContext(ctx)
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Authorization", "Bearer "+fixtureToken)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		writer := httptest.NewRecorder()
		a.ServeHTTP(writer, request)
		done <- writer
	}()
	pendingStop(t, a)
	return done, cancel
}

func pullRename(t *testing.T, a *Adapter, nonce, title string) string {
	t.Helper()
	status, response := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
	if status != http.StatusOK || len(response.Commands) != 1 {
		t.Fatalf("rename not delivered: %d %+v", status, response)
	}
	command := response.Commands[0].(map[string]any)
	id, _ := command["id"].(string)
	if len(command) != 4 || command["hostSessionId"] != fixtureHostSession || command["operation"] != "desktop-rename" || command["title"] != title || !opaque.MatchString(id) {
		t.Fatalf("rename accepted arbitrary command arguments: %+v", command)
	}
	_, again := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
	if len(again.Commands) != 0 {
		t.Fatal("rename delivered twice")
	}
	return id
}

func TestDesktopRenamePrivateAppliedReadbackDoesNotPromoteSession(t *testing.T) {
	a, nonce := discoveryFixture(t)
	r := a.registrations[fixtureSession]
	title, expires, sequence := r.title, r.expires, r.sequence
	capabilities := a.Descriptor().Capabilities
	done, _ := startRenameHTTP(t, a, nonce, "new title")
	a.mu.Lock()
	untouched := r.title == title && r.expires.Equal(expires) && r.sequence == sequence
	a.mu.Unlock()
	if !untouched {
		t.Fatal("queued rename changed projection or lease")
	}
	status, _ := bridgeCall(t, a, "metadata", envelope{SessionID: fixtureSession, Nonce: nonce})
	if status != http.StatusConflict {
		t.Fatal("metadata replaced pending rename")
	}
	id := pullRename(t, a, nonce, "new title")
	status, _ = bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce,
		Result: &modCommandResult{ID: id, OK: true, Rename: &desktopMetadata{Status: "applied", SessionID: fixtureSession, HostSessionID: fixtureHostSession, Title: stringPointer("new title")}}})
	if status != http.StatusOK {
		t.Fatal("applied readback refused")
	}
	status, response := metadataHTTPResult(t, done)
	if status != http.StatusOK || response.Rename == nil || response.Rename.Status != "applied" ||
		response.Rename.SessionID != fixtureSession || response.Rename.Title == nil || *response.Rename.Title != "new title" || response.Metadata != nil || response.ReadOnly {
		t.Fatalf("rename readback: %d %+v", status, response)
	}
	if r.title != title || r.sequence != sequence || r.pending != nil || !reflect.DeepEqual(capabilities, a.Descriptor().Capabilities) {
		t.Fatal("private rename promoted public title, history or capabilities")
	}
	page, err := a.Read(context.Background(), fixtureSession, "", false, nil)
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("rename contaminated conversation history: %+v %v", page, err)
	}
}

func TestDesktopRenameRejectsUnboundOrArbitraryArguments(t *testing.T) {
	a, nonce := discoveryFixture(t)
	for _, fields := range []string{`,"tool":"other"`, `,"session_id":"self"`, `,"_consent":"spoofed"`,
		`,"sequence":1`, `,"events":[]`, `,"result":{}`} {
		request := httptest.NewRequest(http.MethodPost, bridgePrefix+"rename", strings.NewReader(
			`{"sessionId":"`+fixtureSession+`","nonce":"`+nonce+`","title":"new title"`+fields+`}`))
		request.RemoteAddr = "127.0.0.1:1"
		request.Header.Set("Authorization", "Bearer "+fixtureToken)
		writer := httptest.NewRecorder()
		a.ServeHTTP(writer, request)
		if writer.Code != http.StatusBadRequest && writer.Code != http.StatusConflict {
			t.Fatalf("rename accepted arbitrary argument: %s %d", fields, writer.Code)
		}
	}
	for _, tc := range []struct{ remote, token, origin string }{
		{"192.0.2.1:1", fixtureToken, ""}, {"127.0.0.1:1", "wrong", ""}, {"127.0.0.1:1", fixtureToken, "https://example.com"},
	} {
		request := httptest.NewRequest(http.MethodPost, bridgePrefix+"rename", strings.NewReader(`{}`))
		request.RemoteAddr = tc.remote
		request.Header.Set("Authorization", "Bearer "+tc.token)
		request.Header.Set("Origin", tc.origin)
		writer := httptest.NewRecorder()
		a.ServeHTTP(writer, request)
		if writer.Code != http.StatusUnauthorized {
			t.Fatal("rename bypassed private authentication")
		}
	}
	for _, title := range []string{"", "  ", " trim ", strings.Repeat("x", 201), strings.Repeat("🚀", 101)} {
		if validDesktopRenameTitle(title) {
			t.Fatal("invalid native title length admitted")
		}
	}
	if !validDesktopRenameTitle(strings.Repeat("🚀", 100)) {
		t.Fatal("native UTF-16 length limit disagreed with JavaScript")
	}
	for _, adapter := range []*Adapter{a, fixtureAdapter(t)} {
		body, _ := json.Marshal(desktopRenameRequest{envelope: envelope{SessionID: fixtureSession, Nonce: strings.Repeat("z", 43)}, Title: "new title"})
		request := httptest.NewRequest(http.MethodPost, bridgePrefix+"rename", strings.NewReader(string(body)))
		request.RemoteAddr = "127.0.0.1:1"
		request.Header.Set("Authorization", "Bearer "+fixtureToken)
		writer := httptest.NewRecorder()
		adapter.ServeHTTP(writer, request)
		if writer.Code != http.StatusConflict {
			t.Fatal("stale or terminal request queued rename")
		}
	}
}

func TestDesktopRenameCancellationTimeoutAndRebindRemainUnknown(t *testing.T) {
	for _, action := range []string{"cancel", "timeout", "rebind"} {
		t.Run(action, func(t *testing.T) {
			a, nonce := discoveryFixture(t)
			done, cancel := startRenameHTTP(t, a, nonce, "new title")
			id := pullRename(t, a, nonce, "new title")
			if action == "cancel" {
				cancel()
			}
			if action == "rebind" {
				registerOriginFixture(t, a, "desktop", fixtureSession)
			}
			status, response := metadataHTTPResult(t, done)
			if action == "rebind" {
				if status != http.StatusConflict || !strings.Contains(response.Error, "unknown") {
					t.Fatal("rebind did not preserve unknown outcome")
				}
			} else if status != http.StatusOK || response.Rename == nil || response.Rename.Status != "unknown" ||
				response.Rename.SessionID != "" || response.Rename.Title != nil {
				t.Fatalf("canceled or expired rename returned applied evidence: %d %+v", status, response)
			}
			status, _ = bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce,
				Result: &modCommandResult{ID: id, OK: true, Rename: &desktopMetadata{Status: "applied", SessionID: fixtureSession, HostSessionID: fixtureHostSession, Title: stringPointer("new title")}}})
			if status != http.StatusConflict || a.registrations[fixtureSession].pending != nil {
				t.Fatal("late rename crossed request or registration lifetime")
			}
		})
	}
}

func TestDesktopRenameRequiresExactAppliedResult(t *testing.T) {
	a, nonce := discoveryFixture(t)
	r := a.registrations[fixtureSession]
	id := strings.Repeat("n", 43)
	r.pending = &pendingModCommand{command: modCommand{ID: id, Operation: "desktop-rename", Title: "new title", HostSessionID: fixtureHostSession}, desktopIdentity: desktopIdentityFixture(t),
		ctx: context.Background(), changed: make(chan struct{}, 1)}
	pullRename(t, a, nonce, "new title")
	for _, result := range []*modCommandResult{
		{ID: id, OK: true},
		{ID: id, OK: true, Rename: &desktopMetadata{Status: "applied", SessionID: fixtureOther, Title: stringPointer("new title")}},
		{ID: id, OK: true, Rename: &desktopMetadata{Status: "applied", SessionID: fixtureSession, HostSessionID: fixtureHostSession, Title: stringPointer("NEW TITLE")}},
		{ID: id, Rename: &desktopMetadata{Status: "applied", SessionID: fixtureSession, HostSessionID: fixtureHostSession, Title: stringPointer("new title")}},
		{ID: id, OK: true, Rename: &desktopMetadata{Status: "unknown"}},
		{ID: id, Rename: &desktopMetadata{Status: "unknown", Title: stringPointer("new title")}},
		{ID: id, Rename: &desktopMetadata{Status: "matched", SessionID: fixtureSession}},
		{ID: id, Refusal: "private", Rename: &desktopMetadata{Status: "refused"}},
		{ID: id, Metadata: &desktopMetadata{Status: "matched", SessionID: fixtureSession}},
	} {
		status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce, Result: result})
		if status != http.StatusConflict || r.pending.result != nil {
			t.Fatal("invalid rename partially committed")
		}
	}
	for _, state := range []string{"refused", "invalid", "unknown", "busy", "applied"} {
		r.pending.result = nil
		result := &modCommandResult{ID: id, Rename: &desktopMetadata{Status: state}}
		if state == "applied" {
			result.OK = true
			result.Rename.SessionID, result.Rename.HostSessionID = fixtureSession, fixtureHostSession
			result.Rename.Title = stringPointer("new title")
		}
		for i := 0; i < 2; i++ {
			status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce, Result: result})
			if status != http.StatusOK {
				t.Fatal("valid identical rename result rejected")
			}
		}
		result.Rename.Title = stringPointer("changed")
		status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce, Result: result})
		if status != http.StatusConflict {
			t.Fatal("duplicate rename changed evidence")
		}
	}
	r.pending.command.Operation = "desktop-metadata"
	r.pending.result = nil
	status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce,
		Result: &modCommandResult{ID: id, Rename: &desktopMetadata{Status: "unknown"}}})
	if status != http.StatusConflict {
		t.Fatal("rename result contaminated metadata command")
	}
}
