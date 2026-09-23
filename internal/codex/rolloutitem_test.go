package codex

import (
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/nativejson"
)

func TestRolloutItemPreservesNativeTextAliasesAndCommandResult(t *testing.T) {
	value, _, err := nativejson.Read(strings.NewReader(`{"type":"CommandExecution","id":"tool","status":"completed","command":["echo",null,"hello"],"aggregated_output":"done","exit_code":0,"duration":{"secs":2,"nanos":500000000}}`), true)
	if err != nil {
		t.Fatal(err)
	}
	item, err := RolloutItem(value)
	if err != nil {
		t.Fatal(err)
	}
	if nativejson.Text(item["command"]) != "echo  hello" || item["durationMs"] != float64(2500) {
		t.Fatal(item)
	}
	messages := ProjectMessages([]NativeTurn{{ID: "turn", Items: []map[string]any{item}}}, true)
	if len(messages) != 1 || messages[0].Activity.State != "succeeded" {
		t.Fatal(messages)
	}
	if !strings.Contains(string(messages[0].Activity.Result.Value), "done") {
		t.Fatal(messages[0].Activity)
	}
	value, _, err = nativejson.Read(strings.NewReader(`{"type":"AgentMessage","id":"answer","client_id":"input","content":[{"type":"Text","text":"prefix\ud800suffix"},{"type":"Text","text":"next"}]}`), true)
	if err != nil {
		t.Fatal(err)
	}
	item, err = RolloutItem(value)
	if err != nil {
		t.Fatal(err)
	}
	text := nativejson.Text(item["text"])
	if text != "prefix\xed\xa0\x80suffix\nnext" || item["clientId"] != "input" {
		t.Fatalf("Lost native text: %q", text)
	}
}

func TestRolloutItemRejectsMissingIdentityAndDropsUnconfirmedTiming(t *testing.T) {
	for _, value := range []any{nil, "ignored", map[string]any{"type": "Reasoning"}} {
		item, err := RolloutItem(value)
		if err != nil || item != nil {
			t.Fatal(item, err)
		}
	}
	if _, err := RolloutItem(map[string]any{"id": "bad", "type": ""}); err == nil {
		t.Fatal("Empty type accepted")
	}
	item, err := RolloutItem(map[string]any{"id": "tool", "type": "ImageView", "durationMs": 500.0, "duration": map[string]any{"secs": "2", "nanos": float64(0)}, "raw_content": "private"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := item["durationMs"]; ok {
		t.Fatal("Unconfirmed duration retained")
	}
	if _, ok := item["raw_content"]; ok {
		t.Fatal("Raw reasoning retained")
	}
}
