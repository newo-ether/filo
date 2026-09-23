package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/codex"
)

func TestGuardianRPCUsesActualOrderedNativeObjects(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	token := strings.Repeat("a", 64)
	notified := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing private authentication")
			return
		}
		socket, err := codex.UpgradeWebSocket(w, r)
		if err != nil {
			return
		}
		defer socket.Terminate()
		for {
			body, err := socket.ReadMessage()
			if err != nil {
				return
			}
			var request map[string]any
			if json.Unmarshal(body, &request) != nil {
				return
			}
			if request["method"] == "initialized" {
				continue
			}
			var result any = map[string]any{}
			if request["method"] == "thread/read" {
				result = map[string]any{"thread": map[string]any{"id": id, "status": map[string]any{"type": "systemError"}}}
				packet := map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": id,
					"turn": map[string]any{"id": "turn", "status": "failed"}}}
				data, _ := json.Marshal(packet)
				_ = socket.WriteText(data)
			}
			data, _ := json.Marshal(map[string]any{"id": request["id"], "result": result})
			if socket.WriteText(data) != nil {
				return
			}
		}
	}))
	defer server.Close()
	g := &guardianWatch{task: id}
	client, err := connectGuardian("ws"+strings.TrimPrefix(server.URL, "http"), token, func(p map[string]any) {
		g.notification(p)
		notified <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	reply, err := client.request("thread/read", map[string]any{"threadId": id, "includeTurns": false})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := guardianTask(reply, id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-notified:
	case <-time.After(time.Second):
		t.Fatal("notification missing")
	}
	if !g.idle(thread) {
		t.Fatal("ordered native terminal object was ignored")
	}
}
