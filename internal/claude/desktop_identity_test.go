package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureHostSession = "local_30000000-0000-4000-8000-000000000003"

func desktopIdentityFixture(t *testing.T) *desktopIdentity {
	t.Helper()
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	path := filepath.Join(local, "Claude-3p", "claude-code-sessions", "profile", "account", fixtureHostSession+".json")
	writeDesktopIdentityFixture(t, path, fixtureHostSession, fixtureSession)
	return &desktopIdentity{path: path, engineID: fixtureSession, hostID: fixtureHostSession}
}

func writeDesktopIdentityFixture(t *testing.T, path, hostID, engineID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"sessionId": hostID, "cliSessionId": engineID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopIdentityUsesNativeRecordWithoutPrefixArithmetic(t *testing.T) {
	identity := desktopIdentityFixture(t)
	resolved := resolveDesktopIdentity(context.Background(), fixtureSession)
	if resolved == nil || *resolved != *identity || !identity.unchanged() || identity.hostID == "local_"+identity.engineID {
		t.Fatal("native host/engine association was not preserved")
	}
	for _, tc := range []struct{ host, engine string }{
		{fixtureHostSession, fixtureOther}, {"local_" + fixtureSession, fixtureSession},
		{fixtureSession, fixtureSession}, {"local_invalid", fixtureSession},
	} {
		writeDesktopIdentityFixture(t, identity.path, tc.host, tc.engine)
		if resolveDesktopIdentity(context.Background(), fixtureSession) != nil || identity.unchanged() {
			t.Fatal("changed or malformed association accepted")
		}
	}
	writeDesktopIdentityFixture(t, identity.path, fixtureHostSession, fixtureSession)
	duplicate := filepath.Join(filepath.Dir(filepath.Dir(identity.path)), "other-account", fixtureHostSession+".json")
	writeDesktopIdentityFixture(t, duplicate, fixtureHostSession, fixtureSession)
	if resolveDesktopIdentity(context.Background(), fixtureSession) != nil {
		t.Fatal("ambiguous native records accepted")
	}
	if err := os.Remove(duplicate); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if resolveDesktopIdentity(ctx, fixtureSession) != nil {
		t.Fatal("cancelled scan supplied identity")
	}
	if err := os.WriteFile(identity.path, []byte(strings.Repeat("x", (256<<10)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if resolveDesktopIdentity(context.Background(), fixtureSession) != nil || identity.unchanged() {
		t.Fatal("oversized native record supplied identity")
	}
}

func TestDesktopIdentityMappingChangeRejectsMetadataAndRenameResults(t *testing.T) {
	for _, operation := range []string{"desktop-metadata", "desktop-rename"} {
		t.Run(operation, func(t *testing.T) {
			identity := desktopIdentityFixture(t)
			a, nonce := discoveryFixture(t)
			r := a.registrations[fixtureSession]
			id := strings.Repeat("k", 43)
			r.pending = &pendingModCommand{command: modCommand{ID: id, Operation: operation,
				HostSessionID: fixtureHostSession, Title: "new title"}, desktopIdentity: identity,
				ctx: context.Background(), changed: make(chan struct{}, 1)}
			r.pending.delivered = true
			readback := &desktopMetadata{Status: "matched", SessionID: fixtureSession,
				HostSessionID: fixtureHostSession, Title: stringPointer("new title")}
			result := &modCommandResult{ID: id, OK: true, Metadata: readback}
			if operation == "desktop-rename" {
				readback.Status = "applied"
				result.Metadata, result.Rename = nil, readback
			}
			writeDesktopIdentityFixture(t, identity.path, fixtureHostSession, fixtureOther)
			r.pending.delivered = false
			_, pull := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
			if len(pull.Commands) != 0 || r.pending.delivered {
				t.Fatal("changed mapping dispatched a native command")
			}
			r.pending.delivered = true
			status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce, Result: result})
			if status != http.StatusConflict || r.pending.result != nil {
				t.Fatal("changed native association accepted a readback")
			}
		})
	}
}

func TestDesktopIdentityMissingMappingDoesNotDispatch(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	a, nonce := discoveryFixture(t)
	for _, action := range []string{"metadata", "rename"} {
		input := envelope{SessionID: fixtureSession, Nonce: nonce}
		if action == "rename" {
			// The request decoder accepts title only on the fixed rename endpoint.
			request := httptest.NewRequest(http.MethodPost, bridgePrefix+"rename", strings.NewReader(
				`{"sessionId":"`+fixtureSession+`","nonce":"`+nonce+`","title":"new title"}`))
			request.RemoteAddr = "127.0.0.1:1"
			request.Header.Set("Authorization", "Bearer "+fixtureToken)
			writer := httptest.NewRecorder()
			a.ServeHTTP(writer, request)
			var response bridgeResponse
			if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if writer.Code != http.StatusOK || response.Rename == nil || response.Rename.Reason != "mapping-unavailable" {
				t.Fatal("rename without mapping was not bounded")
			}
		} else {
			status, response := bridgeCall(t, a, action, input)
			if status != http.StatusOK || response.Metadata == nil || response.Metadata.Reason != "mapping-unavailable" {
				t.Fatal("metadata without mapping was not bounded")
			}
		}
		if a.registrations[fixtureSession].pending != nil {
			t.Fatal("missing mapping queued a native command")
		}
	}
}
