package codex

import (
	"encoding/json"
	"testing"

	"github.com/newo-ether/filo/internal/nativejson"
)

func TestNativeLiveStateRefreshesNewTurnAndKeepsConcurrentNotification(t *testing.T) {
	fixture := newRpcFixture(t)
	state := &NativeLiveState{Rpc: fixture.rpc, ID: "thread"}
	state.Receive("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "old", "status": "completed"}})
	read := func(turn map[string]any, during func()) map[string]any {
		t.Helper()
		done := make(chan requestResult, 1)
		go func() { value, err := state.State("thread"); done <- requestResult{value, err} }()
		packet := fixture.nextLine(t)
		if packet["method"] != "thread/read" {
			t.Fatalf("unexpected request: %v", packet)
		}
		reply, _ := json.Marshal(map[string]any{"id": packet["id"], "result": map[string]any{"thread": map[string]any{"id": "thread", "status": map[string]any{"type": "idle"}}}})
		fixture.feedLine(t, string(reply))
		packet = fixture.nextLine(t)
		if packet["method"] != "thread/turns/list" {
			t.Fatalf("unexpected request: %v", packet)
		}
		if during != nil {
			during()
		}
		reply, _ = json.Marshal(map[string]any{"id": packet["id"], "result": map[string]any{"data": []any{turn}}})
		fixture.feedLine(t, string(reply))
		result := awaitResult(t, done)
		if result.err != nil {
			t.Fatal(result.err)
		}
		fields, _ := nativejson.Fields(result.value)
		return fields
	}
	value := read(map[string]any{"id": "new", "status": "inProgress"}, nil)
	turn, _ := nativejson.Fields(value["turn"])
	thread, _ := nativejson.Fields(value["thread"])
	status, _ := nativejson.Fields(thread["status"])
	if nativejson.Text(turn["id"]) != "new" || nativejson.Text(status["type"]) != "active" {
		t.Fatal("latest native turn did not replace old cached turn")
	}
	value = read(map[string]any{"id": "new", "status": "completed"}, func() {
		state.Receive("turn/started", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "newer", "status": "inProgress"}})
		state.Receive("item/completed", map[string]any{"threadId": "thread", "turnId": "newer", "item": map[string]any{"type": "userMessage"}})
	})
	turn, _ = nativejson.Fields(value["turn"])
	if nativejson.Text(turn["id"]) != "newer" || value["hasUser"] != true {
		t.Fatal("in-flight read overwrote newer native evidence")
	}
	value = read(map[string]any{"id": "newer", "status": "inProgress"}, nil)
	if value["hasUser"] != true {
		t.Fatal("summary without items erased confirmed user message")
	}
}

func TestNativeLiveStateRejectsDifferentThread(t *testing.T) {
	if _, err := (&NativeLiveState{ID: "expected"}).State("other"); err == nil {
		t.Fatal("accepted another thread")
	}
}
