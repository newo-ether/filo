package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func transcriptFixture(t *testing.T, origin string, count int) (*Adapter, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	directory := filepath.Join(home, "projects", "fixture")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, fixtureSession+".jsonl")
	a := fixtureAdapter(t)
	a.origin = origin
	a.now = func() time.Time { return time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC) }
	status, response := bridgeCall(t, a, "register", registerRequest{SessionID: fixtureSession,
		Instance: "test-instance", Origin: origin, Version: "fixture"})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	var data strings.Builder
	for index := 0; index < count; index++ {
		data.WriteString(transcriptFixtureRow(index, origin))
	}
	if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	status, _ = bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: response.Nonce, Sequence: 1,
		Events: []modEvent{{Kind: "metadata", TranscriptPath: path}}})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	return a, response.Nonce, path
}

func transcriptFixtureRow(index int, origin string) string {
	entrypoint := "cli"
	if origin == "desktop" {
		entrypoint = "claude-desktop-3p"
	}
	return fmt.Sprintf(`{"type":"assistant","uuid":"20000000-0000-4000-8000-%012d","sessionId":%q,"entrypoint":%q,"timestamp":"2026-10-28T23:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"native row %d"}]}}`+"\n", index, fixtureSession, entrypoint, index)
}

func TestTranscriptReadBeforeRegistrationPagesWithoutNativeWritesOrTurns(t *testing.T) {
	for _, origin := range []string{"terminal", "desktop"} {
		t.Run(origin, func(t *testing.T) {
			a, nonce, path := transcriptFixture(t, origin, 300)
			before, _ := os.ReadFile(path)
			status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
				Events: []modEvent{{Kind: "start", TurnID: "actual-live-turn"}, {Kind: "row", UUID: fixtureOther, Role: "user", Text: "live prompt"}}})
			if status != http.StatusOK {
				t.Fatal(status)
			}
			cursor := ""
			seen := map[string]bool{}
			pages := 0
			for {
				page, err := a.Read(context.Background(), fixtureSession, cursor, true, nil)
				if err != nil {
					t.Fatal(err)
				}
				pages++
				if page.Runtime.ActiveTurnID == nil || *page.Runtime.ActiveTurnID != "actual-live-turn" || page.Access.Binding != nonce {
					t.Fatal("history fabricated runtime state or access")
				}
				for _, message := range page.Messages {
					if message.Text == "live prompt" {
						if cursor != "" {
							t.Fatal("live message repeated on an older page")
						}
						continue
					}
					if seen[message.ID] || message.NativeID == "" || message.TurnID != "" {
						t.Fatal("native identity duplicated, lost or assigned an invented turn")
					}
					seen[message.ID] = true
				}
				if page.NextCursor == nil {
					break
				}
				if *page.NextCursor == cursor || pages > 5 {
					t.Fatal("history did not make bounded progress")
				}
				cursor = *page.NextCursor
			}
			if len(seen) != 300 || pages != 3 {
				t.Fatalf("history count=%d pages=%d", len(seen), pages)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("read rewrote native history")
			}
		})
	}
}

func TestTranscriptCursorRejectsRebindingTamperingStoreAndReplacement(t *testing.T) {
	a, _, path := transcriptFixture(t, "terminal", 140)
	page, err := a.Read(context.Background(), fixtureSession, "", false, nil)
	if err != nil || page.NextCursor == nil {
		t.Fatal(err)
	}
	cursor := *page.NextCursor
	if _, err := a.Read(context.Background(), fixtureSession, cursor+"x", false, nil); err == nil {
		t.Fatal("tampered native history cursor accepted")
	}
	state, err := a.decodeTranscriptCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*transcriptCursor){
		func(s *transcriptCursor) { s.Scope = "other-user-store" },
		func(s *transcriptCursor) { s.Origin = "desktop" },
		func(s *transcriptCursor) { s.Session = fixtureOther },
		func(s *transcriptCursor) { s.Binding = "other-binding" },
		func(s *transcriptCursor) { s.Before = -1 },
	} {
		wrong := state
		change(&wrong)
		if _, err := a.Read(context.Background(), fixtureSession, a.encodeTranscriptCursor(wrong), false, nil); err == nil {
			t.Fatal("native history cursor escaped its identity")
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("changed"), state.Before-10); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := a.Read(context.Background(), fixtureSession, cursor, false, nil); err == nil {
		t.Fatal("rewritten native history accepted an old cursor")
	}
	registerFixture(t, a, fixtureSession)
	if _, err := a.Read(context.Background(), fixtureSession, cursor, false, nil); err == nil {
		t.Fatal("old history cursor survived Mod re-registration")
	}
}

func TestTranscriptRowsKeepNativeIdentityAndExcludeForeignOrInternalContent(t *testing.T) {
	cutoff := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC).UnixMilli()
	base := strings.TrimSpace(transcriptFixtureRow(0, "terminal"))
	for _, test := range []struct {
		name string
		row  string
	}{
		{"foreign session", strings.ReplaceAll(base, fixtureSession, fixtureOther)},
		{"foreign source", strings.ReplaceAll(base, `"cli"`, `"claude-desktop-3p"`)},
		{"child", strings.Replace(base, `"type":"assistant"`, `"isSidechain":true,"type":"assistant"`, 1)},
		{"meta", strings.Replace(base, `"type":"assistant"`, `"isMeta":true,"type":"assistant"`, 1)},
		{"missing UUID", strings.Replace(base, `"uuid":"20000000-0000-4000-8000-000000000000",`, "", 1)},
		{"newer than registration", strings.Replace(base, "2026-10-28T23:00:00Z", "2026-10-29T00:00:01Z", 1)},
		{"missing timestamp", strings.Replace(base, `"timestamp":"2026-10-28T23:00:00Z",`, "", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := transcriptMessages([]byte(test.row), fixtureSession, "terminal", cutoff, true, nil); len(got) != 0 {
				t.Fatal("unqualified native row was published")
			}
		})
	}
	row := strings.Replace(base, `{"type":"text","text":"native row 0"}`, `{"type":"thinking","thinking":"native thought","signature":"secret signature"},{"type":"text","text":"native answer"},{"type":"redacted_thinking","data":"secret redaction"}`, 1)
	for _, activity := range []bool{false, true} {
		got := transcriptMessages([]byte(row), fixtureSession, "terminal", cutoff, activity, nil)
		want := 1
		if activity {
			want = 2
		}
		if len(got) != want || got[len(got)-1].Text != "native answer" {
			t.Fatal("native text/thinking projection missing")
		}
		encoded, _ := json.Marshal(got)
		if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), `"turnId":"2000`) {
			t.Fatal("native private data or invented turn escaped")
		}
	}
}

func TestTranscriptUnavailableInitialHistoryPreservesLiveAndRefusesOldCursor(t *testing.T) {
	a, nonce, path := transcriptFixture(t, "terminal", 140)
	page, err := a.Read(context.Background(), fixtureSession, "", false, nil)
	if err != nil || page.NextCursor == nil {
		t.Fatal("missing baseline cursor", err)
	}
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
		Events: []modEvent{{Kind: "row", UUID: fixtureOther, Role: "user", Text: "live prompt"}}})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	assertLive := func() {
		t.Helper()
		live, err := a.Read(context.Background(), fixtureSession, "", false, nil)
		if err != nil || len(live.Messages) != 1 || live.Messages[0].Text != "live prompt" || live.NextCursor != nil {
			t.Fatal("optional history hid live observations", err)
		}
		if _, err := a.Read(context.Background(), fixtureSession, *page.NextCursor, false, nil); err == nil {
			t.Fatal("unavailable native history accepted an older cursor")
		}
	}
	assertLive()
	outside := filepath.Join(t.TempDir(), fixtureSession+".jsonl")
	if err := os.WriteFile(outside, []byte(transcriptFixtureRow(0, "terminal")), 0600); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.registrations[fixtureSession].transcript = outside
	a.mu.Unlock()
	assertLive()
}

func TestTranscriptAppendKeepsSnapshotReplayAndOlderPagesStable(t *testing.T) {
	a, nonce, path := transcriptFixture(t, "terminal", 140)
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
		Events: []modEvent{{Kind: "row", UUID: fixtureOther, Role: "user", Text: "live prompt"}}})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	first, err := a.Read(context.Background(), fixtureSession, "", false, nil)
	if err != nil || first.NextCursor == nil || !first.PageCursor.Known {
		t.Fatal("missing snapshot cursors", err)
	}
	appendRows := func(data string) {
		t.Helper()
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err := file.WriteString(data); err != nil {
			t.Fatal(err)
		}
	}
	partial := strings.TrimSuffix(transcriptFixtureRow(140, "terminal"), "\n")
	appendRows(partial)
	replayed, err := a.Read(context.Background(), fixtureSession, first.PageCursor.Value, false, nil)
	if err != nil || len(replayed.Messages) != len(first.Messages) {
		t.Fatal("append changed initial snapshot replay", err)
	}
	for index, message := range first.Messages {
		if replayed.Messages[index].ID != message.ID {
			t.Fatal("snapshot replay lost a historical or live identity")
		}
	}
	latest, err := a.Read(context.Background(), fixtureSession, "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range latest.Messages {
		if message.Text == "native row 140" {
			t.Fatal("partial native line published")
		}
	}
	appendRows("\n")
	latest, err = a.Read(context.Background(), fixtureSession, "", false, nil)
	if err != nil || len(latest.Messages) < 2 || latest.Messages[len(latest.Messages)-2].Text != "native row 140" {
		t.Fatal("completed native line missing", err)
	}
	older, err := a.Read(context.Background(), fixtureSession, *first.NextCursor, false, nil)
	if err != nil || len(older.Messages) != 12 || older.NextCursor != nil {
		t.Fatal("append changed older page", err)
	}
}

func TestTranscriptBoundedWindowMakesProgressAcrossIgnoredRecords(t *testing.T) {
	a, _, path := transcriptFixture(t, "terminal", 0)
	var data strings.Builder
	data.WriteString(transcriptFixtureRow(0, "terminal"))
	ignored := `{"type":"progress","padding":"` + strings.Repeat("x", 4096) + `"}` + "\n"
	for index := 0; index < maxSessionBytes/len(ignored)+4; index++ {
		data.WriteString(ignored)
	}
	data.WriteString(transcriptFixtureRow(1, "terminal"))
	if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := a.Read(context.Background(), fixtureSession, "", false, nil)
	if err != nil || len(first.Messages) != 1 || first.Messages[0].Text != "native row 1" || first.NextCursor == nil {
		t.Fatal("bounded tail missing", err)
	}
	older, err := a.Read(context.Background(), fixtureSession, *first.NextCursor, false, nil)
	if err != nil || len(older.Messages) != 1 || older.Messages[0].Text != "native row 0" || older.NextCursor != nil {
		t.Fatal("window boundary lost progress", err)
	}
}

func transcriptToolFixtureRow(index int, origin, role, content string) string {
	row := transcriptFixtureRow(index, origin)
	row = strings.Replace(row, `"type":"assistant"`, `"type":"`+role+`"`, 1)
	row = strings.Replace(row, `"role":"assistant"`, `"role":"`+role+`"`, 1)
	return strings.Replace(row, fmt.Sprintf(`[{"type":"text","text":"native row %d"}]`, index), content, 1)
}

func transcriptToolRow(t *testing.T, page protocol.ConversationPage, id string) protocol.Message {
	t.Helper()
	for _, row := range page.Messages {
		if row.ID == id {
			if row.NativeID != id || row.GroupID == "" || row.TurnID != "" || row.Role != "assistant" || row.Activity == nil || row.Activity.Type != "tool" {
				t.Fatal("historical tool lost native identity or invented a turn")
			}
			return row
		}
	}
	t.Fatalf("missing native tool %s", id)
	return protocol.Message{}
}

func TestTranscriptToolsUseNativeIdentityAndSharedPreview(t *testing.T) {
	for _, origin := range []string{"terminal", "desktop"} {
		t.Run(origin, func(t *testing.T) {
			a, _, path := transcriptFixture(t, origin, 0)
			args, _ := json.Marshal(map[string]any{"file_path": "fixture", "content": strings.Repeat("x", 128<<10)})
			output, _ := json.Marshal(map[string]any{"output": strings.Repeat("z", 800000), "exit_code": 7})
			data := transcriptToolFixtureRow(0, origin, "assistant", `[{"type":"thinking","thinking":"native thought","signature":"PRIVATE_SIGNATURE"},{"type":"tool_use","id":"toolu_large","name":"Write","input":`+string(args)+`},{"type":"tool_use","id":"toolu_second","name":"Read","input":{}},{"type":"text","text":"native answer"}]`)
			data += transcriptToolFixtureRow(1, origin, "user", `[{"type":"tool_result","tool_use_id":"toolu_second","content":[{"type":"text","text":"second result"}]},{"type":"tool_result","tool_use_id":"toolu_large","content":`+string(output)+`,"is_error":true},{"type":"tool_result","tool_use_id":"toolu_orphan","content":"ORPHAN_RESULT"},{"type":"text","text":"native user text"}]`)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			page, err := a.Read(context.Background(), fixtureSession, "", true, nil)
			if err != nil || len(page.Messages) != 5 {
				t.Fatal("missing mixed text/thinking/tools", len(page.Messages), err)
			}
			large := transcriptToolRow(t, page, "toolu_large")
			if large.GroupID != "20000000-0000-4000-8000-000000000000" || large.Activity.ToolName != "Write" || large.Activity.State != "failed" || !large.Activity.Arguments.Known || string(large.Activity.Arguments.Value) != string(args) {
				t.Fatal("call identity, full ordinary arguments or native error state changed")
			}
			if !large.Activity.Result.Known || len(large.Activity.Result.Value) > 384<<10 || !json.Valid([]byte(large.Activity.Result.Value)) || !strings.Contains(string(large.Activity.Result.Value), `"exit_code":7`) || !strings.Contains(string(large.Activity.Result.Value), "Filo preview truncated;") || nativeRowBytes(large) > 2<<20 {
				t.Fatal("large result did not use the shared bounded JSON preview")
			}
			second := transcriptToolRow(t, page, "toolu_second")
			if second.Activity.State != "succeeded" || string(second.Activity.Result.Value) != `[{"type":"text","text":"second result"}]` {
				t.Fatal("parallel tool result matched the wrong native call")
			}
			encoded, _ := json.Marshal(page.Messages)
			if strings.Contains(string(encoded), "ORPHAN_RESULT") || strings.Contains(string(encoded), "PRIVATE_SIGNATURE") {
				t.Fatal("orphan result or private signature was published")
			}
			plain, err := a.Read(context.Background(), fixtureSession, "", false, nil)
			if err != nil || len(plain.Messages) != 2 || plain.Messages[0].Text != "native answer" || plain.Messages[1].Text != "native user text" {
				t.Fatal("activity-disabled read lost ordinary text", err)
			}
			for _, row := range plain.Messages {
				if row.Activity != nil {
					t.Fatal("tool activity escaped the activity filter")
				}
			}
			after, _ := os.ReadFile(path)
			if string(after) != data {
				t.Fatal("tool history read changed the native transcript")
			}
		})
	}
}

func TestTranscriptToolsRequireQualifiedUnambiguousResults(t *testing.T) {
	call := transcriptToolFixtureRow(0, "terminal", "assistant", `[{"type":"tool_use","id":"toolu_safe","name":"Read","input":{}}]`)
	result := transcriptToolFixtureRow(1, "terminal", "user", `[{"type":"tool_result","tool_use_id":"toolu_safe","content":"unqualified"}]`)
	for _, test := range []struct {
		name, data string
		omit       bool
	}{
		{"missing result", call, false},
		{"result before call", result + call, false},
		{"wrong tool ID", call + strings.Replace(result, "toolu_safe", "toolu_other", 1), false},
		{"foreign session", call + strings.ReplaceAll(result, fixtureSession, fixtureOther), false},
		{"foreign source", call + strings.Replace(result, `"cli"`, `"claude-desktop-3p"`, 1), false},
		{"sidechain", call + strings.Replace(result, `"type":"user"`, `"isSidechain":true,"type":"user"`, 1), false},
		{"nested", call + strings.Replace(result, `"type":"user"`, `"parent_tool_use_id":"parent","type":"user"`, 1), false},
		{"meta", call + strings.Replace(result, `"type":"user"`, `"isMeta":true,"type":"user"`, 1), false},
		{"missing UUID", call + strings.Replace(result, `"uuid":"20000000-0000-4000-8000-000000000001",`, "", 1), false},
		{"new result", call + strings.Replace(result, "2026-10-28T23:00:00Z", "2026-10-29T00:00:00Z", 1), false},
		{"wrong role", call + strings.Replace(result, `"role":"user"`, `"role":"assistant"`, 1), false},
		{"same envelope UUID", call + strings.Replace(result, "000000000001", "000000000000", 1), false},
		{"duplicate results", call + result + strings.Replace(result, "000000000001", "000000000002", 1), false},
		{"duplicate calls", call + strings.Replace(call, "000000000000", "000000000002", 1) + result, true},
		{"missing call ID", strings.Replace(call, `"id":"toolu_safe",`, "", 1) + result, true},
		{"wrong call role", strings.ReplaceAll(call, `"assistant"`, `"user"`) + result, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, _, path := transcriptFixture(t, "terminal", 0)
			if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
				t.Fatal(err)
			}
			page, err := a.Read(context.Background(), fixtureSession, "", true, nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.omit {
				if len(page.Messages) != 0 {
					t.Fatal("unqualified or ambiguous native call was published")
				}
				return
			}
			tool := transcriptToolRow(t, page, "toolu_safe")
			if len(page.Messages) != 1 || tool.Activity.State != "" || tool.Activity.Result.Known {
				t.Fatal("unqualified/ambiguous result fabricated completion")
			}
		})
	}
}

func TestTranscriptToolsJoinAcrossPagesWithinFixedSnapshot(t *testing.T) {
	a, _, path := transcriptFixture(t, "terminal", 0)
	var data strings.Builder
	data.WriteString(transcriptToolFixtureRow(0, "terminal", "assistant", `[{"type":"tool_use","id":"toolu_done","name":"Read","input":{}},{"type":"tool_use","id":"toolu_pending","name":"Read","input":{}}]`))
	for index := 1; index <= 140; index++ {
		data.WriteString(transcriptFixtureRow(index, "terminal"))
	}
	data.WriteString(transcriptToolFixtureRow(141, "terminal", "user", `[{"type":"tool_result","tool_use_id":"toolu_done","content":"native completion"}]`))
	if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := a.Read(context.Background(), fixtureSession, "", true, nil)
	if err != nil || len(first.Messages) != maxRows || first.NextCursor == nil {
		t.Fatal("missing first bounded page", err)
	}
	for _, row := range first.Messages {
		if row.Activity != nil {
			t.Fatal("result created a second tool identity on the newer page")
		}
	}
	older, err := a.Read(context.Background(), fixtureSession, *first.NextCursor, true, nil)
	if err != nil || older.NextCursor != nil || len(older.Messages) != 14 {
		t.Fatal("older call page missing", err)
	}
	done := transcriptToolRow(t, older, "toolu_done")
	pending := transcriptToolRow(t, older, "toolu_pending")
	if done.Activity.State != "succeeded" || !done.Activity.Result.Known || pending.Activity.State != "" || pending.Activity.Result.Known {
		t.Fatal("cross-page tool result or unknown state incorrect")
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(transcriptToolFixtureRow(142, "terminal", "user", `[{"type":"tool_result","tool_use_id":"toolu_pending","content":"later result","is_error":true}]`))
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := a.Read(context.Background(), fixtureSession, older.PageCursor.Value, true, nil)
	beforeJSON, _ := json.Marshal(older.Messages)
	afterJSON, _ := json.Marshal(replayed.Messages)
	if err != nil || string(beforeJSON) != string(afterJSON) {
		t.Fatal("append changed a signed historical tool snapshot", err)
	}
	fresh, err := a.Read(context.Background(), fixtureSession, "", true, nil)
	if err != nil || fresh.NextCursor == nil {
		t.Fatal("missing fresh tool snapshot", err)
	}
	freshOlder, err := a.Read(context.Background(), fixtureSession, *fresh.NextCursor, true, nil)
	if err != nil || transcriptToolRow(t, freshOlder, "toolu_pending").Activity.State != "failed" {
		t.Fatal("fresh snapshot did not observe the appended native result", err)
	}
	state, err := a.decodeTranscriptCursor(older.PageCursor.Value)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*transcriptCursor){
		func(s *transcriptCursor) { s.Through = s.Before - 1 },
		func(s *transcriptCursor) { s.Through += 1000000 },
		func(s *transcriptCursor) { s.ThroughAnchor = "changed" },
	} {
		wrong := state
		change(&wrong)
		if _, err := a.Read(context.Background(), fixtureSession, a.encodeTranscriptCursor(wrong), true, nil); err == nil {
			t.Fatal("tool lookup escaped the signed file snapshot")
		}
	}
}

func TestTranscriptToolsLeaveOutOfWindowResultUnknown(t *testing.T) {
	a, _, path := transcriptFixture(t, "terminal", 0)
	var data strings.Builder
	data.WriteString(transcriptToolFixtureRow(0, "terminal", "assistant", `[{"type":"tool_use","id":"toolu_distant","name":"Read","input":{}}]`))
	ignored := `{"type":"progress","padding":"` + strings.Repeat("x", 4096) + `"}` + "\n"
	for index := 0; index < (2*maxSessionBytes+(1<<20))/len(ignored)+4; index++ {
		data.WriteString(ignored)
	}
	data.WriteString(transcriptToolFixtureRow(1, "terminal", "user", `[{"type":"tool_result","tool_use_id":"toolu_distant","content":"distant result"}]`))
	if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := a.Read(context.Background(), fixtureSession, "", true, nil)
	if err != nil || len(first.Messages) != 0 || first.NextCursor == nil {
		t.Fatal("orphan result generated a newer display row", err)
	}
	older := first
	for pages := 1; len(older.Messages) == 0 && older.NextCursor != nil && pages < 4; pages++ {
		older, err = a.Read(context.Background(), fixtureSession, *older.NextCursor, true, nil)
		if err != nil {
			t.Fatal("bounded older tool lookup failed", err)
		}
	}
	if older.NextCursor != nil {
		t.Fatal("bounded lookup did not reach the older native call")
	}
	tool := transcriptToolRow(t, older, "toolu_distant")
	if len(older.Messages) != 1 || tool.Activity.State != "" || tool.Activity.Result.Known {
		t.Fatal("out-of-window result invented historical completion")
	}
}

func TestModEffortIsUsableForUnknownRuntimeWithoutModelCatalog(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	canSettings := true
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
		Events: []modEvent{{Kind: "snapshot", CanSettings: &canSettings}}})
	if status != http.StatusOK {
		t.Fatal(status)
	}
	access, err := a.Access(context.Background(), fixtureSession)
	if err != nil || access.Capabilities.Models || len(access.Capabilities.Settings) != 1 {
		t.Fatal("request effort incorrectly requires native models", err)
	}
	ctx := protocol.WithAccessBinding(context.Background(), nonce)
	done := make(chan error, 1)
	go func() {
		done <- a.UpdateSettings(ctx, fixtureSession, protocol.SessionSettings{Effort: protocol.Known("high")})
	}()
	command := mutationCommand(t, a, nonce)
	mutationResult(t, a, nonce, command)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	page, err := a.Read(ctx, fixtureSession, "", false, nil)
	if err != nil || page.Runtime.Status != "unknown" || page.Runtime.SettingsChange.State != "pending" || page.Runtime.Effort.Known {
		t.Fatal("effort selection invented native application or idle status", err)
	}
}
