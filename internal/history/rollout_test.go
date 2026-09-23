package history

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func rolloutFixture(t *testing.T, count int) (string, func(int, any) string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "native.jsonl")
	event := func(index int, item any) string {
		if item == nil {
			item = map[string]any{"id": fmt.Sprint("item-", index), "type": "AgentMessage", "content": []any{map[string]any{"type": "Text", "text": fmt.Sprint("answer-", index)}}}
		}
		turn := "new-turn"
		if index == 0 {
			turn = "old-turn"
		}
		// Native events put their envelope before potentially large item bodies.
		payload := struct {
			Type      string `json:"type"`
			ThreadID  string `json:"thread_id"`
			TurnID    string `json:"turn_id"`
			StartedAt int    `json:"started_at_ms"`
			Item      any    `json:"item"`
		}{"item_completed", "task", turn, index + 1000, item}
		encoded, err := json.Marshal(struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Payload   any    `json:"payload"`
		}{"event_msg", "2026-09-12T00:00:00Z", payload})
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded) + "\n"
	}
	var content strings.Builder
	content.WriteString("{\"type\":\"session_meta\",\"payload\":{\"id\":\"task\"}}\n")
	for i := 0; i < count; i++ {
		content.WriteString(event(i, nil))
	}
	if err := os.WriteFile(path, []byte(content.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return path, event
}

func messageIDs(messages []protocol.Message) []string {
	ids := make([]string, len(messages))
	for i, message := range messages {
		ids[i] = message.ID
	}
	return ids
}

func expectedIDs(start, end int) []string {
	ids := make([]string, 0)
	for i := start; i < end; i++ {
		ids = append(ids, fmt.Sprint("item-", i))
	}
	return ids
}

func appendRollout(t *testing.T, path, value string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(value); err != nil {
		t.Fatal(err)
	}
}

func TestLatestRolloutPagesStayStableAcrossAppendsAndPreserveIndexBoundary(t *testing.T) {
	path, event := rolloutFixture(t, 34)
	before := fileHash(t, path)
	first, err := ReadRolloutPage(context.Background(), path, "task", "", "item-0", true, nil, "captured-index")
	if err != nil || first == nil || !reflect.DeepEqual(messageIDs(first.Messages), expectedIDs(18, 34)) {
		t.Fatalf("Latest page: %+v %v", first, err)
	}
	second, err := ReadRolloutPage(context.Background(), path, "task", *first.NextCursor, "changed-anchor", true, nil, "changed-index")
	if err != nil || !reflect.DeepEqual(messageIDs(second.Messages), expectedIDs(2, 18)) {
		t.Fatalf("Older page: %+v %v", second, err)
	}
	if fileHash(t, path) != before {
		t.Fatal("Read changed original rollout")
	}
	appendRollout(t, path, event(34, nil)+`{"type":"event_msg"`)
	again, err := ReadRolloutPage(context.Background(), path, "task", *first.NextCursor, "different", true, nil, "different")
	if err != nil || !reflect.DeepEqual(again, second) {
		t.Fatalf("Append moved older page: %+v %v", again, err)
	}
	latest, err := ReadRolloutPage(context.Background(), path, "task", "", "item-0", true, nil, "captured-index")
	if err != nil || latest.Messages[len(latest.Messages)-1].ID != "item-34" {
		t.Fatalf("Incomplete append became latest: %+v %v", latest, err)
	}
	third, err := ReadRolloutPage(context.Background(), path, "task", *second.NextCursor, "wrong", true, nil, "wrong")
	if err != nil || !reflect.DeepEqual(messageIDs(third.Messages), expectedIDs(1, 2)) || third.NextCursor == nil || *third.NextCursor != "captured-index" {
		t.Fatalf("Lost original index boundary: %+v %v", third, err)
	}
}

func TestRolloutSkipsHiddenPayloadsAndDeduplicatesCompletedNativeItems(t *testing.T) {
	path, event := rolloutFixture(t, 1)
	tool := map[string]any{"type": "McpToolCall", "id": "tool", "server": "mcp", "tool": "view", "status": "completed", "result": map[string]any{"content": []any{map[string]any{"type": "image", "data": strings.Repeat("x", 2000000)}, map[string]any{"type": "text", "text": strings.Repeat("visible ", 20000)}}}}
	thought := map[string]any{"type": "Reasoning", "id": "thought", "summary_text": []string{"Public summary"}, "raw_content": []string{strings.Repeat("private ", 100000)}}
	appendRollout(t, path, event(1, tool)+event(1, tool)+event(2, thought))
	page, err := ReadRolloutPage(context.Background(), path, "task", "", "item-0", true, nil, "")
	if err != nil || !reflect.DeepEqual(messageIDs(page.Messages), []string{"tool", "thought"}) {
		t.Fatalf("Tool/summary projection: %+v %v", page, err)
	}
	if page.Messages[0].Activity.State != "succeeded" || page.Messages[1].Text != "Public summary" {
		t.Fatal(page.Messages)
	}
	encoded, err := json.Marshal(page.Messages)
	if err != nil || len(encoded) > 20000 || strings.Contains(string(encoded), "private") {
		t.Fatalf("Unbounded/private projection: %d %v", len(encoded), err)
	}
	excluded, err := ReadRolloutPage(context.Background(), path, "task", "", "item-0", true, []string{"new-turn"}, "")
	if err != nil || len(excluded.Messages) != 0 || excluded.NextCursor == nil || *excluded.NextCursor != IndexedHistoryCursor {
		t.Fatalf("Excluded turn affected boundary: %+v %v", excluded, err)
	}
}

func TestRolloutRejectsForeignOrMalformedCursorAndTruncation(t *testing.T) {
	path, _ := rolloutFixture(t, 34)
	page, err := ReadRolloutPage(context.Background(), path, "task", "", "item-0", true, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(*page.NextCursor, rolloutPrefix))
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(data, &base); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{"id": "other", "file": "foreign", "before": -1, "anchor": nil, "indexedCursor": 2} {
		fields := make(map[string]any)
		for key, value := range base {
			fields[key] = value
		}
		fields[key] = value
		encoded, _ := json.Marshal(fields)
		cursor := rolloutPrefix + base64.RawURLEncoding.EncodeToString(encoded)
		if _, err := ReadRolloutPage(context.Background(), path, "task", cursor, "item-0", true, nil, ""); err == nil {
			t.Fatal("Malformed cursor accepted", key)
		}
	}
	delete(base, "before")
	encoded, _ := json.Marshal(base)
	if _, err := ReadRolloutPage(context.Background(), path, "task", rolloutPrefix+base64.RawURLEncoding.EncodeToString(encoded), "item-0", true, nil, ""); err == nil {
		t.Fatal("Missing cursor offset accepted")
	}
	if err := os.Truncate(path, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRolloutPage(context.Background(), path, "task", *page.NextCursor, "item-0", true, nil, ""); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatal(err)
	}
}

func TestRolloutScanBudgetAndIdentityAreIndependentOfNativeIndex(t *testing.T) {
	path, _ := rolloutFixture(t, 1)
	appendRollout(t, path, strings.Repeat("{\"type\":\"unrelated\"}\n", 513))
	page, err := ReadRolloutPage(context.Background(), path, "task", "", "item-0", true, nil, "")
	if err != nil || page != nil {
		t.Fatalf("Initial no-event scan did not permit indexed read: %+v %v", page, err)
	}
	if _, err := ReadRolloutPage(context.Background(), path, "other", "", "item-0", true, nil, ""); err == nil {
		t.Fatal("Foreign native history accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadRolloutPage(ctx, path, "task", "", "item-0", true, nil, ""); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestRolloutInvalidTimeRetainsSerializableNativeMessage(t *testing.T) {
	path, _ := rolloutFixture(t, 1)
	appendRollout(t, path, `{"type":"event_msg","timestamp":"invalid","payload":{"type":"item_completed","thread_id":"task","turn_id":"turn","item":{"type":"AgentMessage","id":"answer","content":[{"type":"Text","text":"Still visible"}]}}}`+"\n")
	page, err := ReadRolloutPage(context.Background(), path, "task", "", "item-0", true, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(page.Messages)
	if err != nil {
		t.Fatal("Native invalid time made the entire page unencodable", err)
	}
	var wire []map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 1 || wire[0]["timestamp"] != nil || wire[0]["text"] != "Still visible" {
		t.Fatal(string(encoded))
	}
}
