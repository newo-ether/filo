package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func f64p(v float64) *float64 { return &v }

func msgJSON(t *testing.T, m protocol.Message) map[string]any {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func activityOf(t *testing.T, m protocol.Message) map[string]any {
	t.Helper()
	json := msgJSON(t, m)
	activity, ok := json["activity"].(map[string]any)
	if !ok {
		t.Fatalf("message has no activity: %v", json)
	}
	return activity
}

func TestProjectMessagesReversesTurnsAndShapesIdentities(t *testing.T) {
	turns := []NativeTurn{
		{ID: "t1", StartedAt: f64p(1), Items: []map[string]any{
			{"id": "u1", "type": "userMessage", "clientId": "c", "content": []any{
				map[string]any{"type": "text", "text": "hello"},
				"raw-string-dropped",
				map[string]any{"type": "image", "text": "also-dropped"},
				map[string]any{"type": "text"},
			}},
			{"id": "a1", "type": "agentMessage", "text": "answer"},
		}},
		{ID: "t2", StartedAt: f64p(2.5), Items: []map[string]any{
			{"id": "a2", "type": "agentMessage"},
		}},
	}
	messages := ProjectMessages(turns, false)
	if len(messages) != 3 {
		t.Fatalf("messages = %d", len(messages))
	}
	// Newest turn first; items keep source order inside a turn.
	if messages[0].ID != "a2" || messages[1].ID != "u1" || messages[2].ID != "a1" {
		t.Fatalf("order = %v %v %v", messages[0].ID, messages[1].ID, messages[2].ID)
	}
	if messages[0].Timestamp != 2500 {
		t.Fatalf("timestamp = %v", messages[0].Timestamp)
	}
	// text ?? '' on agentMessage: missing text becomes empty.
	if messages[0].Text != "" {
		t.Fatalf("missing text = %q", messages[0].Text)
	}
	if messages[1].Role != "user" {
		t.Fatalf("role = %v", messages[1].Role)
	}
	// inputText keeps only type-text parts (missing text contributes ""),
	// joined with single newlines.
	if messages[1].Text != "hello\n" {
		t.Fatalf("inputText = %q", messages[1].Text)
	}
	if messages[1].ClientID == nil || *messages[1].ClientID != "c" {
		t.Fatalf("clientId = %v", messages[1].ClientID)
	}
	if messages[2].ClientID != nil {
		t.Fatalf("absent clientId must serialize null, got %v", messages[2].ClientID)
	}
	// activity key absent entirely without an activity.
	if _, exists := msgJSON(t, messages[2])["activity"]; exists {
		t.Fatal("unexpected activity key")
	}
}

func TestProjectMessagesReasoningActivity(t *testing.T) {
	turns := []NativeTurn{{ID: "t", Items: []map[string]any{
		{"id": "skip", "type": "reasoning", "summary": []any{"  ", ""}},
		{"id": "r", "type": "reasoning", "summary": []any{" first ", "second"}, "durationMs": 12.9},
	}}}
	messages := ProjectMessages(turns, true)
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	if messages[0].Text != " first \n\nsecond" {
		t.Fatalf("text = %q", messages[0].Text)
	}
	activity := activityOf(t, messages[0])
	if activity["type"] != "thought" {
		t.Fatalf("activity = %v", activity)
	}
	if activity["durationMs"] != float64(12) { // Math.trunc
		t.Fatalf("durationMs = %v", activity["durationMs"])
	}
	// Without the activity channel reasoning items are invisible.
	if len(ProjectMessages(turns, false)) != 0 {
		t.Fatal("reasoning leaked without includeActivity")
	}
}

func TestToolActivityCommandExecution(t *testing.T) {
	activity, ok := toolActivity(map[string]any{
		"type": "commandExecution", "command": "ls", "cwd": "/tmp",
		"aggregatedOutput": "files", "exitCode": float64(0), "status": "completed",
	})
	if !ok {
		t.Fatal("missing activity")
	}
	if activity.ToolName != "execute_shell_command" || activity.State != "succeeded" {
		t.Fatalf("activity = %+v", activity)
	}
	if activity.Arguments.Value != `{"command":"ls","cwd":"/tmp"}` {
		t.Fatalf("arguments = %q", activity.Arguments.Value)
	}
	if activity.Result.Value != `{"output":"files","exit_code":0}` {
		t.Fatalf("result = %q", activity.Result.Value)
	}
	// Non-zero exit marks failure; exitCode stays in the result object.
	activity, _ = toolActivity(map[string]any{
		"type": "commandExecution", "exitCode": float64(2), "aggregatedOutput": "boom",
	})
	if activity.State != "failed" {
		t.Fatalf("state = %v", activity.State)
	}
	// No output and no exitCode: result stays undefined (dropped).
	activity, _ = toolActivity(map[string]any{"type": "commandExecution", "command": "x"})
	if activity.Result.Known {
		t.Fatalf("result leaked: %q", activity.Result.Value)
	}
	// exitCode null is not "present non-null" and typeof null != number.
	activity, _ = toolActivity(map[string]any{"type": "commandExecution", "exitCode": nil, "status": "inProgress"})
	if activity.State != "running" || activity.Result.Known {
		t.Fatalf("null exitCode changed semantics: %+v", activity)
	}
}

func TestToolActivityFileChange(t *testing.T) {
	activity, _ := toolActivity(map[string]any{
		"type": "fileChange", "status": "completed",
		"changes": []any{map[string]any{"path": "F:/x"}},
	})
	if activity.ToolName != "file_edit" || activity.State != "succeeded" {
		t.Fatalf("activity = %+v", activity)
	}
	if !strings.Contains(string(activity.Result.Value), `"path":"F:/x"`) {
		t.Fatalf("result = %q", activity.Result.Value)
	}
	// Not completed: arguments still carry changes, result absent.
	activity, _ = toolActivity(map[string]any{"type": "fileChange", "status": "inProgress"})
	if activity.Result.Known || activity.State != "running" {
		t.Fatalf("activity = %+v", activity)
	}
}

func TestToolActivityMcpToolCall(t *testing.T) {
	activity, _ := toolActivity(map[string]any{
		"type": "mcpToolCall", "server": "svc", "tool": "do",
		"arguments": map[string]any{"a": float64(1)},
		"result": map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": "one"}, map[string]any{"type": "text", "text": "two"}},
			"structuredContent": map[string]any{"k": "v"},
		},
		"status": "completed",
	})
	if activity.ToolName != "svc/do" {
		t.Fatalf("toolName = %v", activity.ToolName)
	}
	if activity.Result.Value != `{"text":"one\n\ntwo","structuredContent":{"k":"v"}}` {
		t.Fatalf("result = %q", activity.Result.Value)
	}
	// isError flips the gate even with a completed status.
	activity, _ = toolActivity(map[string]any{
		"type": "mcpToolCall", "server": "svc", "tool": "do",
		"result": map[string]any{"isError": true}, "status": "completed",
	})
	if activity.State != "failed" {
		t.Fatalf("state = %v", activity.State)
	}
	// item.error wins over the result object via ?? and marks failure. A
	// top-level string preview stays raw (TS append, no JSON quotes).
	activity, _ = toolActivity(map[string]any{
		"type": "mcpToolCall", "error": "native failure", "result": map[string]any{"content": []any{}},
	})
	if activity.Result.Value != "native failure" || activity.State != "failed" {
		t.Fatalf("error precedence broken: %+v", activity)
	}
	// Missing server/tool fall back inside the template.
	activity, _ = toolActivity(map[string]any{"type": "mcpToolCall"})
	if activity.ToolName != "mcp/tool" {
		t.Fatalf("toolName = %v", activity.ToolName)
	}
	// null result keeps result undefined.
	activity, _ = toolActivity(map[string]any{"type": "mcpToolCall", "result": nil})
	if activity.Result.Known {
		t.Fatalf("null result leaked: %q", activity.Result.Value)
	}
	// Numeric server names coerce with JS string rules in the template.
	activity, _ = toolActivity(map[string]any{"type": "mcpToolCall", "server": float64(7), "tool": false})
	if activity.ToolName != "7/false" {
		t.Fatalf("coerced toolName = %v", activity.ToolName)
	}
}

func TestToolActivityDynamicAndCollab(t *testing.T) {
	activity, _ := toolActivity(map[string]any{
		"type": "dynamicToolCall", "namespace": "ns", "tool": "t",
		"contentItems": []any{map[string]any{"type": "inputText", "text": "x"}, map[string]any{"type": "inputText", "text": "y"}},
	})
	if activity.ToolName != "ns/t" {
		t.Fatalf("toolName = %v", activity.ToolName)
	}
	if activity.Result.Value != "x\n\ny" {
		t.Fatalf("result = %q", activity.Result.Value)
	}
	// Empty namespace and tool collapse to the fallback name.
	activity, _ = toolActivity(map[string]any{"type": "dynamicToolCall", "namespace": "", "tool": ""})
	if activity.ToolName != "dynamicToolCall" {
		t.Fatalf("toolName = %v", activity.ToolName)
	}
	activity, _ = toolActivity(map[string]any{"type": "collabAgentToolCall", "tool": "spawn", "prompt": "p", "agentsStates": []any{}})
	if activity.ToolName != "collaboration/spawn" {
		t.Fatalf("toolName = %v", activity.ToolName)
	}
	if activity.Result.Value != "[]" {
		t.Fatalf("agentsStates result = %q", activity.Result.Value)
	}
}

func TestToolActivityWebSearchImageViewGeneration(t *testing.T) {
	// webSearch with results succeeds without an explicit status.
	activity, _ := toolActivity(map[string]any{"type": "webSearch", "query": "q", "results": []any{"r"}})
	if activity.ToolName != "web_search" || activity.State != "succeeded" {
		t.Fatalf("activity = %+v", activity)
	}
	if activity.Result.Value != `{"results":["r"]}` {
		t.Fatalf("result = %q", activity.Result.Value)
	}
	// imageView: non-empty path marks success and exposes imagePath.
	activity, _ = toolActivity(map[string]any{"type": "imageView", "path": "C:/work/img.png"})
	if activity.ToolName != "view_image" || activity.State != "succeeded" {
		t.Fatalf("activity = %+v", activity)
	}
	if activity.ImagePath.Value != "C:/work/img.png" || !activity.ImagePath.Known {
		t.Fatalf("imagePath = %+v", activity.ImagePath)
	}
	// imageView with an empty path: state stays undefined but imagePath ""
	// is still an explicit own key on the TS wire shape.
	activity, _ = toolActivity(map[string]any{"type": "imageView", "path": ""})
	if activity.State != "" {
		t.Fatalf("state = %q", activity.State)
	}
	if !activity.ImagePath.Known || activity.ImagePath.Value != "" {
		t.Fatalf("imagePath presence lost: %+v", activity.ImagePath)
	}
	// imageGeneration failure marks failed and becomes the result (raw
	// top-level string preview).
	activity, _ = toolActivity(map[string]any{"type": "imageGeneration", "failure": "no", "revisedPrompt": "p"})
	if activity.State != "failed" || activity.Result.Value != "no" {
		t.Fatalf("activity = %+v", activity)
	}
	activity, _ = toolActivity(map[string]any{"type": "imageGeneration", "savedPath": "C:/out.png"})
	if activity.Result.Value != `{"savedPath":"C:/out.png"}` {
		t.Fatalf("result = %q", activity.Result.Value)
	}
	// Empty savedPath is falsy: no result object.
	activity, _ = toolActivity(map[string]any{"type": "imageGeneration", "savedPath": ""})
	if activity.Result.Known {
		t.Fatalf("falsy savedPath leaked: %q", activity.Result.Value)
	}
	// Unknown item types have no projection.
	if _, ok := toolActivity(map[string]any{"type": "mystery"}); ok {
		t.Fatal("unknown type produced activity")
	}
}

func TestToolActivityStateChainAndGuards(t *testing.T) {
	// success===false marks failure before status.
	activity, _ := toolActivity(map[string]any{"type": "imageView", "path": "p", "success": false})
	if activity.State != "failed" {
		t.Fatalf("state = %v", activity.State)
	}
	// success===true does not clear other failure gates.
	activity, _ = toolActivity(map[string]any{"type": "imageView", "path": "p", "success": false, "status": "declined"})
	if activity.State != "failed" {
		t.Fatalf("state = %v", activity.State)
	}
	activity, _ = toolActivity(map[string]any{"type": "imageView", "path": "p", "status": "interrupted"})
	if activity.State != "stopped" {
		t.Fatalf("state = %v", activity.State)
	}
	activity, _ = toolActivity(map[string]any{"type": "imageView", "path": "p", "status": "in_progress"})
	if activity.State != "running" {
		t.Fatalf("state = %v", activity.State)
	}
	// durationMs guards: negative, fractional, huge and non-number inputs.
	activity, _ = toolActivity(map[string]any{"type": "imageView", "path": "p", "durationMs": -1.0})
	if activity.DurationMs.Known {
		t.Fatal("negative duration accepted")
	}
	activity, _ = toolActivity(map[string]any{"type": "imageView", "path": "p", "durationMs": 9007199254740993.0})
	if activity.DurationMs.Known {
		t.Fatal("unsafe-integer duration accepted")
	}
	activity, _ = toolActivity(map[string]any{"type": "imageView", "path": "p", "durationMs": "12"})
	if activity.DurationMs.Known {
		t.Fatal("string duration accepted")
	}
}

func TestTurnErrorProjection(t *testing.T) {
	failed := NativeTurn{ID: "t9", Status: "failed", StartedAt: f64p(3), Items: []map[string]any{},
		Error: map[string]any{"message": "  wirebreak\U0000ffff  "}}
	messages := ProjectMessages([]NativeTurn{failed}, false)
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	message := messages[0]
	if message.ID != "filo-turn-error:t9" || message.TurnID != "t9" {
		t.Fatalf("identity = %+v", message)
	}
	if message.Timestamp != 3000 {
		t.Fatalf("timestamp = %v", message.Timestamp)
	}
	if !message.Error.Known || !message.Error.Value {
		t.Fatalf("error flag = %+v", message.Error)
	}
	// The control-character strip removes \u0007 and \u001f but keeps the
	// noncharacter; trim then removes the surrounding spaces.
	if message.Text != "wirebreak\U0000ffff" {
		t.Fatalf("text = %q", message.Text)
	}
	// Non-string or missing messages fall back to the generic text.
	for _, bad := range []any{nil, "string", map[string]any{"message": 5}, map[string]any{}} {
		messages := ProjectMessages([]NativeTurn{{ID: "t", Status: "failed", Error: bad}}, false)
		if len(messages) != 1 || messages[0].Text != "Codex failed to complete this turn." {
			t.Fatalf("fallback failed for %v: %+v", bad, messages)
		}
	}
	// Empty after trim/slice pipeline also falls back.
	messages = ProjectMessages([]NativeTurn{{ID: "t", Status: "failed", Error: map[string]any{"message": "\u0007 "}}}, false)
	if messages[0].Text != "Codex failed to complete this turn." {
		t.Fatalf("empty pipeline fallback = %q", messages[0].Text)
	}
	// Non-failed turns never synthesize the error message.
	if len(ProjectMessages([]NativeTurn{{ID: "t", Status: "completed", Error: map[string]any{"message": "x"}}}, false)) != 0 {
		t.Fatal("error leaked on completed turn")
	}
}

func TestTurnErrorSlicesByUtf16Units(t *testing.T) {
	// 8191 BMP chars + one more BMP char exactly fill the 8192-unit budget.
	long := strings.Repeat("a", 8191) + "\U0000FFFD"
	messages := ProjectMessages([]NativeTurn{{ID: "t", Status: "failed",
		Error: map[string]any{"message": long}}}, false)
	if string(messages[0].Text) != long {
		t.Fatalf("full-budget text was truncated")
	}
	// 8191 BMP chars followed by a surrogate pair: JS .slice(0,8192) keeps
	// the leading (high) surrogate of the cut pair as an unpaired unit; the
	// WTF-8 bytes for U+D83D are ED A0 B4.
	half := strings.Repeat("a", 8191) + "\U0001F600tail"
	messages = ProjectMessages([]NativeTurn{{ID: "t", Status: "failed",
		Error: map[string]any{"message": half}}}, false)
	got := string(messages[0].Text)
	want := strings.Repeat("a", 8191) + string([]byte{0xED, 0xA0, 0xBD})
	if got != want {
		t.Fatalf("cut result mismatch: len=%d tail=%q", len(got), got[len(got)-8:])
	}
}

func TestProjectMessagesTruncatesOversizedActivityPreview(t *testing.T) {
	huge := strings.Repeat("x", 9000)
	turns := []NativeTurn{{ID: "t", Items: []map[string]any{
		{"type": "commandExecution", "command": huge, "status": "completed"},
	}}}
	messages := ProjectMessages(turns, true)
	activity := activityOf(t, messages[0])
	args, _ := activity["arguments"].(string)
	if !strings.HasPrefix(args, "{\"command\":\"xxxx") || !strings.Contains(args, "Filo preview truncated") {
		t.Fatalf("preview budget not applied: %d chars", len(args))
	}
	if len([]rune(args)) > 8300 {
		t.Fatalf("preview far beyond budget: %d", len(args))
	}
}
