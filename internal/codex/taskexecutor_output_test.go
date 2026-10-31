package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTaskExecutorForwardsNativeToolOutputBeyondClientRequestBound(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{admittedID: executorTaskID})
	headers := http.Header{"Authorization": {"Bearer " + executorToken()}}
	socket, err := DialWebSocket(context.Background(), f.url, headers, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Terminate()
	output := strings.Repeat("x", executorMaxPayload+512*1024)
	f.notify("item/completed", map[string]any{
		"threadId": executorTaskID,
		"turnId":   "turn",
		"item": map[string]any{
			"type": "commandExecution", "id": "command", "command": "build",
			"aggregatedOutput": output, "exitCode": 0, "status": "completed",
		},
	})
	_ = socket.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	packet, err := socket.ReadMessage()
	if err != nil {
		t.Fatal("Native output was rejected by the client request bound", err)
	}
	var response struct {
		Method string `json:"method"`
		Params struct {
			Item struct {
				Output string `json:"aggregatedOutput"`
			} `json:"item"`
		} `json:"params"`
	}
	if err := json.Unmarshal(packet, &response); err != nil {
		t.Fatal("Native JSON was clipped", err)
	}
	if response.Method != "item/completed" || response.Params.Item.Output != output {
		t.Fatal("Native tool output changed in forwarding")
	}
}
