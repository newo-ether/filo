package codex

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

// taskActivityMessage builds one persisted message of the live turn.
func taskActivityMessage(id string, text ...string) protocol.Message {
	body := id
	if len(text) > 0 {
		body = text[0]
	}
	return protocol.Message{
		MessageIdentity: protocol.MessageIdentity{ID: id, TurnID: "turn", Role: "assistant",
			Timestamp: 1000},
		Text: protocol.Text(body),
	}
}

// taskActivityItem builds one native agent message item.
func taskActivityItem(id string, text ...string) map[string]any {
	body := ""
	if len(text) > 0 {
		body = text[0]
	}
	return map[string]any{"id": id, "type": "agentMessage", "text": body}
}

// taskActivityFixture is one live buffer already started on its turn.
func taskActivityFixture() (*TaskActivity, func(string, map[string]any)) {
	live := NewTaskActivity("task")
	live.Receive("turn/started", map[string]any{"threadId": "task",
		"turn": map[string]any{"id": "turn", "startedAt": 1.0}})
	return live, func(method string, extra map[string]any) {
		params := map[string]any{"threadId": "task", "turnId": "turn"}
		for key, value := range extra {
			params[key] = value
		}
		live.Receive(method, params)
	}
}

// taskActivityIDs lists the ids of one merged page.
func taskActivityIDs(messages []protocol.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}

func TestTaskActivityKeepsNativeBlockOrderAndNeverReplacesACompletedItem(t *testing.T) {
	live, event := taskActivityFixture()
	event("item/started", map[string]any{"item": taskActivityItem("a")})
	event("item/started", map[string]any{"item": taskActivityItem("b")})
	event("item/agentMessage/delta", map[string]any{"itemId": "a", "delta": "first"})
	if ids := taskActivityIDs(live.Merge(nil, pointer("turn"), true, false)); len(ids) != 2 ||
		ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("the live block order = %v", ids)
	}
	event("item/completed", map[string]any{"item": taskActivityItem("a", "first")})
	event("item/agentMessage/delta", map[string]any{"itemId": "a", "delta": "late"})
	persisted := []protocol.Message{taskActivityMessage("a", "native complete")}
	merged := live.Merge(persisted, pointer("turn"), true, false)
	if ids := taskActivityIDs(merged); len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("the persisted page and its live tail = %v", ids)
	}
	if merged[0].Text != "native complete" {
		t.Fatalf("a completed item was replaced: %+v", merged[0])
	}
	// A page of another turn is returned untouched.
	other := []protocol.Message{taskActivityMessage("a")}
	if !reflect.DeepEqual(live.Merge(other, nil, true, false), other) {
		t.Fatal("a page without a current turn was merged")
	}
}

func TestTaskActivityBridgesTheCompletedTailWithoutPrependingOldBlocks(t *testing.T) {
	live, event := taskActivityFixture()
	for _, id := range []string{"a", "b", "c"} {
		event("item/completed", map[string]any{"item": taskActivityItem(id, id)})
	}
	stored := []protocol.Message{taskActivityMessage("b")}
	if ids := taskActivityIDs(live.Merge(stored, pointer("turn"), true, false)); len(ids) != 2 ||
		ids[0] != "b" || ids[1] != "c" {
		t.Fatalf("the bridged tail = %v", ids)
	}
	if !reflect.DeepEqual(stored, []protocol.Message{taskActivityMessage("b")}) {
		t.Fatal("the merge mutated the persisted page")
	}
	newer := []protocol.Message{taskActivityMessage("newer")}
	if !reflect.DeepEqual(live.Merge(newer, pointer("turn"), true, false), newer) {
		t.Fatal("a completed tail was prepended to a persisted page")
	}
	// An older turn may not reopen this buffer.
	live.Receive("item/completed", map[string]any{"threadId": "task", "turnId": "old",
		"item": taskActivityItem("stale")})
	ids := taskActivityIDs(live.Merge(stored, pointer("turn"), true, false))
	if len(ids) != 2 || ids[0] != "b" || ids[1] != "c" {
		t.Fatalf("an older turn changed the buffer: %v", ids)
	}
}

func TestTaskActivityKeepsSteeringInputAfterEarlierLiveBlocks(t *testing.T) {
	for _, method := range []string{"item/agentMessage/delta", "item/reasoning/summaryTextDelta"} {
		t.Run(method, func(t *testing.T) {
			live, event := taskActivityFixture()
			user := func(id string) map[string]any {
				return map[string]any{"id": id, "type": "userMessage", "content": []any{
					map[string]any{"type": "text", "text": id},
				}}
			}
			event("item/completed", map[string]any{"item": user("first-user")})
			event(method, map[string]any{"itemId": "earlier-live", "delta": "before steer"})
			event("item/completed", map[string]any{"item": user("steering-user")})
			event("item/agentMessage/delta", map[string]any{"itemId": "later-live", "delta": "after steer"})
			persisted := []protocol.Message{taskActivityMessage("first-user"), taskActivityMessage("steering-user")}
			for index := range persisted {
				persisted[index].Role = "user"
			}
			// Native history can publish accepted input while an earlier response
			// or reasoning block still exists only in the live stream.
			page := live.Merge(persisted, pointer("turn"), true, false)
			want := []string{"first-user", "earlier-live", "steering-user", "later-live"}
			if got := taskActivityIDs(page); !reflect.DeepEqual(got, want) {
				t.Fatalf("input moved before earlier live activity: got %v, want %v", got, want)
			}
			// Once history catches up, the same native order must remain visible.
			if got := taskActivityIDs(live.Merge(page, pointer("turn"), true, true)); !reflect.DeepEqual(got, want) {
				t.Fatalf("persisted catch-up changed input order: got %v, want %v", got, want)
			}
		})
	}
}

func TestTaskActivityKeepsReasoningParagraphsAndTheActivityOptIn(t *testing.T) {
	live, event := taskActivityFixture()
	event("item/reasoning/summaryTextDelta", map[string]any{"itemId": "r", "summaryIndex": 0.0, "delta": "First"})
	event("item/reasoning/summaryTextDelta", map[string]any{"itemId": "r", "summaryIndex": 1.0, "delta": "Second"})
	event("item/reasoning/summaryTextDelta", map[string]any{"itemId": "r", "summaryIndex": 0.0, "delta": " paragraph"})
	merged := live.Merge(nil, pointer("turn"), true, false)
	if len(merged) != 1 || merged[0].Text != "First paragraph\n\nSecond" {
		t.Fatalf("the reasoning summary = %+v", merged)
	}
	if merged[0].Activity == nil || merged[0].Activity.Type != "thought" {
		t.Fatalf("the reasoning activity = %+v", merged[0].Activity)
	}
	if plain := live.Merge(nil, pointer("turn"), false, false); len(plain) != 0 {
		t.Fatalf("the activity opt-out returned %+v", plain)
	}
}

func TestTaskActivityOverflowNeverPublishesATruncatedSuffix(t *testing.T) {
	live := NewTaskActivityWith("task", 2, 500)
	send := func(delta string) {
		live.Receive("item/agentMessage/delta", map[string]any{"threadId": "task",
			"turnId": "turn", "itemId": "a", "delta": delta})
	}
	send(strings.Repeat("x", 600))
	send("suffix")
	if merged := live.Merge(nil, pointer("turn"), true, false); len(merged) != 0 {
		t.Fatalf("an overflowed buffer published %+v", merged)
	}
	persisted := []protocol.Message{taskActivityMessage("a", "full original")}
	merged := live.Merge(persisted, pointer("turn"), true, false)
	if len(merged) != 1 || merged[0].Text != "full original" {
		t.Fatalf("an overflowed buffer replaced the persisted page: %+v", merged)
	}
}

func TestTaskActivityAcceptsTransportDecodedPayloads(t *testing.T) {
	// The regression this pins: a live notification reaches the buffer as decoded
	// native JSON, whose nested objects are the transport's own object type rather
	// than a plain Go map. A buffer that only accepts plain maps silently drops
	// every item event of a real transport, so the decoded shape is the shape the
	// test must use.
	live := NewTaskActivity("task")
	events := []struct{ method, params string }{
		{"turn/started", `{"threadId":"task","turn":{"id":"turn","startedAt":1}}`},
		{"item/completed", `{"threadId":"task","turnId":"turn","item":{"id":"u","type":"userMessage",` +
			`"clientId":"client","content":[{"type":"text","text":"hello"}]}}`},
		{"item/started", `{"threadId":"task","turnId":"turn","item":{"id":"a","type":"agentMessage","text":""}}`},
		{"item/agentMessage/delta", `{"threadId":"task","turnId":"turn","itemId":"a","delta":"stream"}`},
		{"item/reasoning/summaryTextDelta", `{"threadId":"task","turnId":"turn","summaryIndex":0,` +
			`"itemId":"r","delta":"first"}`},
		{"item/completed", `{"threadId":"task","turnId":"turn","item":{"id":"r","type":"reasoning",` +
			`"summary":["first","second"],"durationMs":250}}`},
		{"item/completed", `{"threadId":"task","turnId":"turn","item":{"id":"a","type":"agentMessage",` +
			`"text":"stream"}}`},
	}
	for _, event := range events {
		params, _, err := nativejson.Read(strings.NewReader(event.params), false)
		if err != nil {
			t.Fatalf("decode %s: %v", event.method, err)
		}
		live.Receive(event.method, params)
	}
	merged := live.Merge(nil, pointer("turn"), true, false)
	if ids := taskActivityIDs(merged); len(ids) != 3 || ids[0] != "u" || ids[1] != "a" || ids[2] != "r" {
		t.Fatalf("the transport-decoded blocks = %v", ids)
	}
	if merged[0].Text != "hello" || merged[0].Role != "user" || merged[0].ClientID == nil ||
		*merged[0].ClientID != "client" {
		t.Fatalf("the user message = %+v", merged[0])
	}
	if merged[1].Text != "stream" {
		t.Fatalf("the streamed item = %+v", merged[1])
	}
	if merged[2].Text != "first\n\nsecond" || merged[2].Activity == nil ||
		merged[2].Activity.Type != "thought" {
		t.Fatalf("the reasoning item = %+v", merged[2])
	}
	if duration := merged[2].Activity.DurationMs; !duration.Known || duration.Value != 250 {
		t.Fatalf("the reasoning duration = %+v", merged[2].Activity.DurationMs)
	}
}

func TestTaskActivitySnapshotsDoNotExposeRetainedOrPersistedPayloads(t *testing.T) {
	live, event := taskActivityFixture()
	event("item/completed", map[string]any{"item": map[string]any{
		"id": "user", "type": "userMessage", "clientId": "original", "content": []any{},
	}})
	event("item/reasoning/summaryTextDelta", map[string]any{"itemId": "thought", "delta": "reasoning"})
	first := live.Merge(nil, pointer("turn"), true, false)
	*first[0].ClientID = "changed"
	first[1].Activity.Type = "changed"
	first[1].Text = "changed"
	second := live.Merge(nil, pointer("turn"), true, false)
	if *second[0].ClientID != "original" || second[1].Activity.Type != "thought" || second[1].Text != "reasoning" {
		t.Fatalf("a returned page mutated live state: %+v", second)
	}
	persisted := []protocol.Message{{MessageIdentity: protocol.MessageIdentity{ID: "persisted", ClientID: pointer("client")},
		ImageLinks: []string{"image"}, Activity: &protocol.Activity{Type: "tool"}}}
	for _, turn := range []*string{nil, pointer("turn")} {
		page := live.Merge(persisted, turn, true, false)
		*page[0].ClientID = "changed"
		page[0].ImageLinks[0] = "changed"
		page[0].Activity.Type = "changed"
		page[0].ID = "changed"
		if *persisted[0].ClientID != "client" || persisted[0].ImageLinks[0] != "image" ||
			persisted[0].Activity.Type != "tool" || persisted[0].ID != "persisted" {
			t.Fatalf("a returned page mutated persisted input: %+v", persisted)
		}
	}
}

func TestTaskActivityConcurrentReceiveAndMerge(t *testing.T) {
	live, event := taskActivityFixture()
	const iterations = 500
	var workers sync.WaitGroup
	start := make(chan struct{})
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < iterations; index++ {
			event("item/reasoning/summaryTextDelta", map[string]any{"itemId": "thought", "delta": "x"})
			event("item/agentMessage/delta", map[string]any{"itemId": "answer", "delta": "x"})
		}
	}()
	for reader := 0; reader < 4; reader++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for index := 0; index < iterations; index++ {
				page := live.Merge(nil, pointer("turn"), true, false)
				for _, message := range page {
					if message.Activity != nil {
						message.Activity.Type = "reader-owned"
					}
				}
			}
		}()
	}
	close(start)
	workers.Wait()
	page := live.Merge(nil, pointer("turn"), true, false)
	if len(page) != 2 || page[0].Activity.Type != "thought" ||
		len(page[0].Text) != iterations || len(page[1].Text) != iterations {
		t.Fatalf("concurrent projection lost or exposed state: %+v", page)
	}
}

func TestTaskActivityIgnoresOtherTasksAndOlderTurns(t *testing.T) {
	live, event := taskActivityFixture()
	// Another task of the same executor is never observed.
	live.Receive("item/completed", map[string]any{"threadId": "other", "turnId": "turn",
		"item": taskActivityItem("foreign", "foreign")})
	// An unknown method never adds a block.
	event("item/agentMessage/changed", map[string]any{"itemId": "unknown", "delta": "ignored"})
	// A delta of a completed item never reopens it.
	event("item/completed", map[string]any{"item": taskActivityItem("done", "done")})
	event("item/agentMessage/delta", map[string]any{"itemId": "done", "delta": "late"})
	merged := live.Merge(nil, pointer("turn"), true, false)
	if ids := taskActivityIDs(merged); len(ids) != 1 || ids[0] != "done" {
		t.Fatalf("the retained blocks = %v", ids)
	}
	if merged[0].Text != "done" {
		t.Fatalf("a completed item was extended: %q", merged[0].Text)
	}
}
