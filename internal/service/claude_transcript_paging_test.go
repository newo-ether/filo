package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/claude"
	"github.com/newo-ether/filo/internal/protocol"
)

func TestClaudeTranscriptPublicPagingReplayAndAppend(t *testing.T) {
	for _, scenario := range []struct {
		name               string
		budget, historical bool
	}{
		{name: "row-limit"},
		{name: "byte-limit-through-worker", budget: true},
		{name: "historical-tools-through-worker", budget: true, historical: true},
	} {
		budget, historical := scenario.budget, scenario.historical
		t.Run(scenario.name, func(t *testing.T) {
			const session = "10000000-0000-4000-8000-000000000001"
			const liveID = "10000000-0000-4000-8000-000000000002"
			const turn = "public-paging-native-turn"
			home := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", home)
			directory := filepath.Join(home, "projects", "paging-fixture")
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, session+".jsonl")
			stamp := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
			expected := map[string]string{liveID: "live prompt"}
			const doneTool = "toolu_public_done"
			const pendingTool = "toolu_public_pending"
			row := func(index int) (string, string, []byte) {
				id := fmt.Sprintf("20000000-0000-4000-8000-%012d", index)
				text := fmt.Sprintf("historical message %d", index)
				if budget {
					text += strings.Repeat("x", 15000)
				}
				role := "assistant"
				content := []any{map[string]any{"type": "text", "text": text}}
				if historical && index == 0 {
					for _, toolID := range []string{doneTool, pendingTool} {
						content = append(content, map[string]any{"type": "tool_use", "id": toolID, "name": "Read", "input": map[string]any{"file_path": "fixture"}})
						expected[toolID] = ""
					}
				}
				if historical && (index == 299 || index == 300) {
					role = "user"
					toolID := doneTool
					if index == 300 {
						toolID = pendingTool
					}
					content = append(content, map[string]any{"type": "tool_result", "tool_use_id": toolID, "content": "native tool result", "is_error": index == 300})
				}
				data, err := json.Marshal(map[string]any{
					"type": role, "uuid": id, "sessionId": session,
					"entrypoint": "cli", "timestamp": stamp,
					"message": map[string]any{"role": role, "content": content},
				})
				if err != nil {
					t.Fatal(err)
				}
				return id, text, append(data, '\n')
			}
			var transcript bytes.Buffer
			for index := 0; index < 300; index++ {
				id, text, data := row(index)
				expected[id] = text
				transcript.Write(data)
			}
			if err := os.WriteFile(path, transcript.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			native, err := claude.New(claude.Config{
				Token: testToken, NativeScopeID: "public-transcript-scope", Origin: "terminal", TTL: time.Minute,
			})
			if err != nil {
				t.Fatal(err)
			}
			registered := claudeTranscriptBridge(t, native, "register", map[string]any{
				"sessionId": session, "instance": "public-paging-fixture", "origin": "terminal",
			})
			nonce, ok := registered["nonce"].(string)
			if !ok || nonce == "" {
				t.Fatal("native registration did not return a binding")
			}
			events := []any{
				map[string]any{"kind": "metadata", "transcriptPath": path},
				map[string]any{"kind": "start", "turnId": turn},
				map[string]any{"kind": "row", "uuid": liveID, "role": "user", "text": expected[liveID]},
			}
			toolValue := `"` + strings.Repeat(`\u0001`, 60000) + `"`
			if budget {
				for index := 0; index < 2; index++ {
					toolID := fmt.Sprintf("public-paging-tool-%d", index)
					expected["mod-tool:"+toolID] = ""
					events = append(events, map[string]any{"kind": "block", "turnId": turn,
						"block": map[string]any{"step": 0, "index": index, "type": "tool",
							"nativeId": toolID, "toolName": "Read", "state": "succeeded",
							"arguments": toolValue, "result": toolValue}})
				}
			}
			claudeTranscriptBridge(t, native, "push", map[string]any{
				"sessionId": session, "nonce": nonce, "sequence": 1, "events": events,
			})
			var source ConversationReader = native
			if budget {
				// Exercise the same display pager again at the private-worker boundary.
				source = readerFunc(func(ctx context.Context, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
					return ReadConversationPage(ctx, native, id, cursor, PageOptions{Activity: activity, Layer: "worker", ExcludedTurns: excluded})
				})
			}
			options := PageOptions{Activity: true, IncludeMetadata: true}
			seen := map[string]bool{}
			parts := map[string]map[int]string{}
			cursor, pages := "", 0
			var first protocol.ConversationPage
			var appendedID string
			inner, older := false, false
			for {
				page, err := ReadConversationPage(context.Background(), source, session, cursor, options)
				if err != nil {
					t.Fatal("public read failed", err)
				}
				pages++
				encoded, err := EncodeResponse(page)
				if err != nil || len(encoded) > MaxConversationPageBytes || len(page.Messages) > maxMessages || len(page.Messages) == 0 {
					t.Fatal("public page exceeded its byte/row bounds or lost progress", err)
				}
				if page.Access == nil || page.Access.Binding != nonce || page.Runtime == nil ||
					page.Runtime.ActiveTurnID == nil || *page.Runtime.ActiveTurnID != turn {
					t.Fatal("history changed live binding or runtime")
				}
				if pages == 1 {
					first = page
					appended, _, data := row(300)
					appendedID = appended
					file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
					if err != nil {
						t.Fatal(err)
					}
					_, writeErr := file.Write(data)
					closeErr := file.Close()
					if writeErr != nil || closeErr != nil {
						t.Fatal(writeErr, closeErr)
					}
					if budget && len(page.Messages) >= maxMessages {
						t.Fatal("fixture did not force a byte-limited page")
					}
				}
				if !page.PageCursor.Known {
					t.Fatal("public page has no replay cursor")
				}
				replay, err := ReadConversationPage(context.Background(), source, session, page.PageCursor.Value, options)
				if err != nil || !bytes.Equal(Marshal(replay.Messages), Marshal(page.Messages)) {
					t.Fatal("public replay changed message identities or bodies after append", err)
				}
				for _, message := range page.Messages {
					if seen[message.ID] {
						t.Fatal("duplicate public message identity", message.ID)
					}
					seen[message.ID] = true
					id := message.ID
					if strings.HasPrefix(id, "filo-part:") {
						id = message.NativeID
					}
					if _, ok := expected[id]; !ok {
						t.Fatal("unexpected or newly appended identity leaked into the snapshot", id)
					}
					if id != liveID && !strings.HasPrefix(id, "mod-tool:") && (message.NativeID != id || message.TurnID != "") {
						t.Fatal("history lost native UUID or invented a turn")
					}
					if message.Activity != nil && message.Activity.Type == "tool" {
						if strings.HasPrefix(id, "toolu_public_") {
							if message.NativeID != id || message.GroupID != "20000000-0000-4000-8000-000000000000" || message.TurnID != "" || message.Activity.ToolName != "Read" ||
								!message.Activity.Arguments.Known || string(message.Activity.Arguments.Value) != `{"file_path":"fixture"}` {
								t.Fatal("public paging changed historical call identity or arguments")
							}
							if id == doneTool && (message.Activity.State != "succeeded" || !message.Activity.Result.Known || message.Activity.Result.Value != `"native tool result"`) {
								t.Fatal("public older page lost its newer native tool result")
							}
							if id == pendingTool && (message.Activity.State != "" || message.Activity.Result.Known) {
								t.Fatal("append changed a pending historical tool in the fixed snapshot")
							}
						} else if message.Activity.Arguments.Value != protocol.Text(toolValue) || message.Activity.Result.Value != protocol.Text(toolValue) {
							t.Fatal("public paging changed live tool arguments or results")
						}
					}
					if parts[id] == nil {
						parts[id] = map[int]string{}
					}
					offset := message.TextOffset.Value
					if _, exists := parts[id][offset]; exists {
						t.Fatal("duplicate native text range")
					}
					parts[id][offset] = string(message.Text)
				}
				if page.NextCursor == nil {
					break
				}
				inner = inner || strings.HasPrefix(*page.NextCursor, "filo.page.")
				older = older || strings.HasPrefix(*page.NextCursor, "claude.transcript.")
				if *page.NextCursor == cursor || pages > 20 {
					t.Fatal("public cursor did not make bounded progress")
				}
				cursor = *page.NextCursor
			}
			if !inner || !older || len(parts) != len(expected) {
				t.Fatalf("incomplete pagination: inner=%t older=%t rows=%d want=%d", inner, older, len(parts), len(expected))
			}
			for id, want := range expected {
				offsets := make([]int, 0, len(parts[id]))
				for offset := range parts[id] {
					offsets = append(offsets, offset)
				}
				sort.Ints(offsets)
				var body strings.Builder
				for _, offset := range offsets {
					if offset != body.Len() {
						t.Fatal("omitted or overlapping native text range", id)
					}
					body.WriteString(parts[id][offset])
				}
				if body.String() != want {
					t.Fatal("native text was omitted or changed", id)
				}
			}
			replay, err := ReadConversationPage(context.Background(), source, session, first.PageCursor.Value, options)
			if err != nil || !bytes.Equal(Marshal(replay.Messages), Marshal(first.Messages)) {
				t.Fatal("first public snapshot changed after older traversal", err)
			}
			latest, err := native.Read(context.Background(), session, "", false, nil)
			if err != nil {
				t.Fatal(err)
			}
			foundAppend := false
			for _, message := range latest.Messages {
				foundAppend = foundAppend || message.ID == appendedID
			}
			if !foundAppend {
				t.Fatal("fresh history read did not observe the completed append")
			}
			t.Logf("public pages=%d native identities=%d display identities=%d; replay and append stable", pages, len(parts), len(seen))
		})
	}
}

func claudeTranscriptBridge(t *testing.T, native *claude.Adapter, action string, body any) map[string]any {
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
