package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/claude"
	"github.com/newo-ether/filo/internal/protocol"
)

func TestClaudeDesktopRenameThroughWorkerAndEncryptedGateway(t *testing.T) {
	const sessionID = "10000000-0000-4000-8000-000000000001"
	const hostID = "local_30000000-0000-4000-8000-000000000003"
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	directory := filepath.Join(local, "Claude-3p", "claude-code-sessions", "profile", "account")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, hostID+".json")
	if err := os.WriteFile(path, []byte(`{"sessionId":"`+hostID+`","cliSessionId":"`+sessionID+`","title":"old title"}`), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := claude.New(claude.Config{Token: testToken, NativeScopeID: "desktop-rename-scope", Origin: "desktop", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	registration := claudeTranscriptBridge(t, native, "register", map[string]any{
		"sessionId": sessionID, "instance": "public-rename-fixture", "origin": "desktop",
	})
	nonce := registration["nonce"].(string)
	private, err := NewServer(ServerOptions{Token: testToken, Identity: &ServiceIdentity{SessionMode: "standalone"}, Adapters: []Adapter{{
		ID: claudeDesktopSource, Family: "claude", Origin: "desktop", NativeScopeID: "desktop-rename-scope",
		Sessions: native, Capabilities: claude.DesktopCapabilities(),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(private)
	defer endpoint.Close()
	worker, err := NewUserWorker(endpoint.URL, StaticUserWorkerToken(testToken), nil, "", DefaultUserWorkerTimeouts)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	adapters := gatewaySourceAdapters([]string{claudeDesktopSource}, worker, func(string, string) {}, Adapter{}, nil)
	_, base, client := encryptedAdapterServer(t, adapters, nil)
	root := base + "/v1/adapters/" + claudeDesktopSource + "/sessions/" + sessionID
	read := func() protocol.ConversationPage {
		t.Helper()
		code, data := adapterRequest(t, client, http.MethodGet, root, nil, nil)
		var page protocol.ConversationPage
		if code != http.StatusOK || json.Unmarshal(data, &page) != nil || page.Access == nil {
			t.Fatal("public conversation", code, string(data))
		}
		return page
	}
	page := read()
	if !page.Access.Capabilities.Rename || page.Access.Capabilities.Create || page.Access.Capabilities.Archive || page.Access.Capabilities.Uploads || page.Access.Capabilities.Models {
		t.Fatal("public Desktop capability ceiling", page.Access)
	}
	binding := page.Access.Binding
	for _, bad := range []any{nil, map[string]any{"name": "new title", "accessBinding": "stale"}} {
		code, _ := adapterRequest(t, client, http.MethodPost, root+"/rename", bad, nil)
		if code < 400 {
			t.Fatal("public rename accepted missing/stale access", code)
		}
	}
	type response struct {
		status int
		data   []byte
	}
	done := make(chan response, 1)
	go func() {
		code, data := adapterRequest(t, client, http.MethodPost, root+"/rename",
			map[string]any{"name": "new title", "accessBinding": binding}, nil)
		done <- response{code, data}
	}()
	var command map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for command == nil && time.Now().Before(deadline) {
		select {
		case result := <-done:
			t.Fatal("public mutation refused before dispatch", result.status, string(result.data))
		default:
		}
		pulled := claudeTranscriptBridge(t, native, "pull", map[string]any{"sessionId": sessionID, "nonce": nonce})
		if commands, ok := pulled["commands"].([]any); ok && len(commands) == 1 {
			command = commands[0].(map[string]any)
			break
		}
		time.Sleep(time.Millisecond)
	}
	if command == nil || len(command) != 4 || command["operation"] != "desktop-rename" || command["hostSessionId"] != hostID || command["title"] != "new title" {
		t.Fatal("fixed native command not delivered through worker", command)
	}
	if current := read(); current.Runtime.Title == nil || *current.Runtime.Title != "old title" {
		t.Fatal("public title changed before applied readback", current)
	}
	claudeTranscriptBridge(t, native, "result", map[string]any{"sessionId": sessionID, "nonce": nonce,
		"result": map[string]any{"id": command["id"], "ok": true, "rename": map[string]any{
			"status": "applied", "sessionId": sessionID, "hostSessionId": hostID, "title": "new title",
		}},
	})
	select {
	case result := <-done:
		var session protocol.Session
		if result.status != http.StatusOK || json.Unmarshal(result.data, &session) != nil || session.ID != sessionID || session.Title != "new title" || session.UpdatedAt <= 0 {
			t.Fatal("public applied response", result.status, string(result.data))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("public rename did not finish")
	}
	page = read()
	if page.Runtime.Title == nil || *page.Runtime.Title != "new title" || len(page.Messages) != 0 {
		t.Fatal("public readback lost title or added conversation history", page)
	}
	pulled := claudeTranscriptBridge(t, native, "pull", map[string]any{"sessionId": sessionID, "nonce": nonce})
	if commands, _ := pulled["commands"].([]any); len(commands) != 0 {
		t.Fatal("public mutation replayed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	page = read()
	if page.Access.Capabilities.Rename {
		t.Fatal("missing mapping retained public rename grant")
	}
	code, _ := adapterRequest(t, client, http.MethodPost, root+"/rename", map[string]any{"name": "must not run", "accessBinding": binding}, nil)
	if code < 400 {
		t.Fatal("mapping removal retained public mutation authority")
	}
	if _, err := native.Rename(context.Background(), sessionID, "must not run"); err == nil {
		t.Fatal("direct caller bypassed access binding")
	}
}
