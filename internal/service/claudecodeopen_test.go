package service

import (
	"bytes"
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

func TestClaudeCodeOpenThroughExecutorWorkerAndEncryptedGateway(t *testing.T) {
	const sessionID = "10000000-0000-4000-8000-000000000001"
	const messageID = "20000000-0000-4000-8000-000000000002"
	const assistantID = "20000000-0000-4000-8000-000000000003"
	const toolID = "toolu_code_open_fixture"
	const scope = "claude-code-open-scope"
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	project := filepath.Join(home, "projects", "open-fixture")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(project, sessionID+".jsonl")
	stamp := time.Now().Add(-time.Hour).UTC().Truncate(time.Second).Add(283 * time.Millisecond)
	var data []byte
	for _, row := range []struct {
		id, role string
		content  any
	}{
		{messageID, "user", "existing native prompt"},
		{assistantID, "assistant", []any{
			map[string]any{"type": "thinking", "thinking": "existing native thought"},
			map[string]any{"type": "tool_use", "id": toolID, "name": "Read", "input": map[string]any{"file_path": "fixture"}},
			map[string]any{"type": "text", "text": "existing native answer"},
		}},
	} {
		encoded, err := json.Marshal(map[string]any{
			"type": row.role, "uuid": row.id, "sessionId": sessionID,
			"entrypoint": "cli", "timestamp": stamp.Format(time.RFC3339Nano),
			"message": map[string]any{"role": row.role, "content": row.content},
		})
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(transcript, data, 0600); err != nil {
		t.Fatal(err)
	}
	native, err := claude.New(claude.Config{Token: testToken, NativeScopeID: scope, Origin: "terminal", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	registration := claudeCodeOpenBridge(t, native, "register", map[string]any{
		"sessionId": sessionID, "instance": "code-open-fixture", "origin": "terminal",
	})
	claudeCodeOpenBridge(t, native, "push", map[string]any{
		"sessionId": sessionID, "nonce": registration["nonce"], "sequence": 1,
		"events": []any{map[string]any{"kind": "metadata", "transcriptPath": transcript}},
	})
	root := t.TempDir()
	launches := 0
	executor, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{
		Directory: filepath.Join(root, "tasks"), Workspace: root,
		UploadDirectory: filepath.Join(root, "uploads"), NativeScopeID: scope, ReadOnly: native,
		Launch: func(string) error { launches++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	private, err := NewServer(ServerOptions{Token: testToken, Identity: &ServiceIdentity{SessionMode: "standalone"}, Adapters: []Adapter{{
		ID: claudeCodeSource, Family: "claude", Origin: "cli", NativeScopeID: scope,
		Sessions: executor, Capabilities: executor.Capabilities(),
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
	adapters := gatewaySourceAdapters([]string{claudeCodeSource}, worker, func(string, string) {}, Adapter{}, nil)
	_, base, client := encryptedAdapterServer(t, adapters, nil)
	catalogURL := base + "/v1/adapters/" + claudeCodeSource + "/sessions"
	code, body := adapterRequest(t, client, http.MethodGet, catalogURL, nil, nil)
	var catalog protocol.SessionPage
	if code != http.StatusOK || json.Unmarshal(body, &catalog) != nil || len(catalog.Sessions) != 1 || catalog.Sessions[0].ID != sessionID {
		t.Fatal("public catalog", code, string(body))
	}
	for _, query := range []string{"?includeActivity=true", "?includeMetadata=true", "?includeActivity=true&includeMetadata=true"} {
		t.Run(query, func(t *testing.T) {
			code, body := adapterRequest(t, client, http.MethodGet, catalogURL+"/"+sessionID+query, nil, nil)
			var page protocol.ConversationPage
			if code != http.StatusOK || json.Unmarshal(body, &page) != nil {
				t.Fatal("public conversation open", code, string(body))
			}
			if page.Access == nil || page.Access.NativeScopeID != scope || page.Access.Binding == "" || page.Access.CanonicalSessionRef == "" {
				t.Fatal("public conversation lost its qualified access", page.Access)
			}
			want := map[string]protocol.Text{
				messageID:                  "existing native prompt",
				assistantID:                "existing native answer",
				assistantID + ":thought:0": "existing native thought",
				toolID:                     "",
			}
			if len(page.Messages) != len(want) {
				t.Fatal("public conversation lost native history", string(body))
			}
			for _, message := range page.Messages {
				text, found := want[message.ID]
				if !found || message.Text != text {
					t.Fatal("public conversation changed native history", message.ID)
				}
			}
			if query != "?includeActivity=true" {
				if len(page.Nodes) != len(page.Messages) {
					t.Fatal("public conversation lost the message metadata required by Agora", string(body))
				}
				for index, node := range page.Nodes {
					if node.ID != page.Messages[index].ID {
						t.Fatal("public conversation changed metadata identity", node.ID)
					}
				}
			}
			// Decode the wire timestamps as Agora's nullable integer fields, not protocol.Number.
			type timedRow struct {
				ID        string `json:"id"`
				Timestamp *int64 `json:"timestamp"`
			}
			var wire struct {
				Messages []timedRow `json:"messages"`
				Nodes    []timedRow `json:"nodes"`
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatal("public timestamps are not compatible with integer clients", err)
			}
			for _, row := range append(wire.Messages, wire.Nodes...) {
				if row.Timestamp == nil || *row.Timestamp != stamp.UnixMilli() {
					t.Fatal("public history must retain native integer milliseconds", row.ID, row.Timestamp)
				}
			}
			if page.Runtime == nil || page.Runtime.ActiveTurnID != nil || page.Access.Capabilities.Create {
				t.Fatal("opening history invented execution authority", string(body))
			}
		})
	}
	if launches != 0 {
		t.Fatal("opening an existing Mod conversation started a broker", launches)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks")); !os.IsNotExist(err) {
		t.Fatal("opening history allocated private executor state", err)
	}
}

func claudeCodeOpenBridge(t *testing.T, native *claude.Adapter, action string, body any) map[string]any {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/claude-mod/v1/"+action, bytes.NewReader(data))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Authorization", "Bearer "+testToken)
	writer := httptest.NewRecorder()
	native.ServeHTTP(writer, request)
	if writer.Code != http.StatusOK {
		t.Fatalf("synthetic native %s returned HTTP%d", action, writer.Code)
	}
	var response map[string]any
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}
