package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
)

// This opt-in qualification mutates only a new, tool-free native thread. It
// never connects to a user's running server or writes to a terminal's input.
func TestCLISharedNativeQualification(t *testing.T) {
	if os.Getenv("FILO_CLI_SHARED_PROBE") != "1" {
		t.Skip("explicit shared native CLI qualification is disabled")
	}
	runtimePath, err := resolveCLIRuntime()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(tokenBytes)
	tokenPath := filepath.Join(root, "native-token")
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := fmt.Sprintf("ws://127.0.0.1:%d", listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	command := exec.Command(runtimePath, "app-server", "--listen", endpoint,
		"--ws-auth", "capability-token", "--ws-token-file", tokenPath)
	command.Dir = root
	command.SysProcAttr = detachedProcessAttributes()
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	// Only this test-created native process is cleaned up. Every turn is observed
	// to completion before normal cleanup, and no user process is signaled.
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	connect := func() *codex.CodexHost {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			host, err := codex.ConnectCodexHost(context.Background(), endpoint,
				codex.HostOptions{Token: &token, Timeout: time.Second})
			if err == nil {
				return host
			}
			if time.Now().After(deadline) {
				t.Fatal("dedicated app-server did not become ready")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	first, second := connect(), connect()
	defer first.Close()
	defer second.Close()
	firstEvents, secondEvents := &sharedProbeEvents{}, &sharedProbeEvents{}
	first.Rpc.SetHandlers(nil, firstEvents.receive)
	second.Rpc.SetHandlers(nil, secondEvents.receive)
	created, err := first.Rpc.Request("thread/start", map[string]any{
		"cwd": root, "approvalPolicy": "never", "sandbox": "read-only",
		"developerInstructions": "Reply exactly OK. Do not call any tools.",
	})
	if err != nil {
		t.Fatal("dedicated native thread/start failed")
	}
	createdFields := sharedProbeObject(created)
	id, _ := sharedProbeObject(createdFields["thread"])["id"].(string)
	if !uuidPattern.MatchString(id) {
		t.Fatal("native thread/start did not return a thread identity")
	}
	// A new native thread is lazy: it has no rollout until its first input.
	initial, err := first.Rpc.Request("turn/start", map[string]any{"threadId": id,
		"input": []any{map[string]any{"type": "text", "text": "Reply exactly OK. Do not call tools."}}})
	if err != nil {
		t.Fatal("dedicated initial native turn failed; no retry")
	}
	initialTurn, _ := sharedProbeObject(sharedProbeObject(initial)["turn"])["id"].(string)
	initialDeadline := time.Now().Add(90 * time.Second)
	for !firstEvents.has("turn/completed", id, initialTurn) && time.Now().Before(initialDeadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !firstEvents.has("turn/completed", id, initialTurn) {
		t.Fatal("initial native turn did not complete")
	}
	t.Logf("initial completion status=%s assistant-OK=%v", firstEvents.turnStatus(initialTurn), firstEvents.assistantOK(initialTurn))
	if !firstEvents.assistantOK(initialTurn) {
		t.Fatalf("initial native turn did not produce the required OK response: %q", firstEvents.assistantText(initialTurn))
	}
	loaded, err := second.Rpc.Request("thread/loaded/list", map[string]any{})
	if err != nil || !sharedProbeContains(sharedProbeObject(loaded)["data"], id) {
		t.Fatal("second connection cannot identify the live loaded thread")
	}
	t.Log("native loaded/list identifies the same live thread without scanning history")
	resumed, err := second.Rpc.Request("thread/resume", map[string]any{"threadId": id, "excludeTurns": true})
	if err != nil {
		t.Fatalf("second connection cannot subscribe to the already-loaded thread: %v", err)
	}
	if sharedProbeObject(sharedProbeObject(resumed)["thread"])["id"] != id {
		t.Fatal("second connection subscription returned a different native identity")
	}
	t.Log("second native connection resumed the exact already-loaded thread")
	surface, closeLease := sharedProbeSurface(t, root, first, endpoint, token)
	firstLease := closeLease
	page, err := surface.List(context.Background(), "")
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].ID != id || page.Sessions[0].Access == nil || !page.Sessions[0].Access.Capabilities.IdleSend {
		t.Fatalf("production live catalog did not admit the same native thread: rows=%d err=%v", len(page.Sessions), err)
	}
	model, _ := createdFields["model"].(string)
	currentEffort, _ := createdFields["reasoningEffort"].(string)
	effort := "low"
	if currentEffort == effort {
		effort = "high"
	}
	err = surface.UpdateSettings(context.Background(), id, protocol.SessionSettings{Effort: protocol.Known(effort)})
	if err != nil {
		t.Fatal("native settings/update rejected the requested effort; no retry")
	}
	settingsDeadline := time.Now().Add(5 * time.Second)
	for (!firstEvents.settingsApplied(id, effort) || !secondEvents.settingsApplied(id, effort)) && time.Now().Before(settingsDeadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !firstEvents.settingsApplied(id, effort) || !secondEvents.settingsApplied(id, effort) {
		t.Fatal("settings acknowledgment lacks the matching applied value on both native clients")
	}
	t.Logf("matching settings value reached both native clients; model-known=%v", model != "")
	clientID, err := randomUUID()
	if err != nil {
		t.Fatal(err)
	}
	receipt := sharedSurfaceReceipt(t, surface, id, "Reply exactly OK. Do not call tools.", clientID)
	turnID := receipt.TurnID
	if turnID == "" {
		t.Fatal("native turn/start omitted its exact turn identity")
	}
	// Disconnect the initiating/mobile connection. The independent native owner
	// must still finish and publish to the terminal-equivalent subscriber.
	second.Close()
	surface.shared.Close()
	firstLease()
	deadline := time.Now().Add(90 * time.Second)
	for !firstEvents.has("turn/completed", id, turnID) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !firstEvents.has("turn/completed", id, turnID) {
		t.Fatal("exact native turn did not finish after initiating-client disconnect")
	}
	if !firstEvents.receipt(id, turnID, clientID) {
		t.Fatal("other subscriber lacks an exact native client/turn/message receipt")
	}
	t.Logf("shared completion status=%s assistant-OK=%v", firstEvents.turnStatus(turnID), firstEvents.assistantOK(turnID))
	if !firstEvents.assistantOK(turnID) {
		t.Fatal("shared native turn did not produce the required OK response")
	}
	t.Log("same-thread user-message receipt and exact turn completion reached the other subscriber after sender disconnect")
	stopClient := connect()
	defer stopClient.Close()
	_, err = stopClient.Rpc.Request("thread/resume", map[string]any{"threadId": id, "excludeTurns": true})
	if err != nil {
		t.Fatal("dedicated stop subscriber failed to reconnect")
	}
	surface, closeLease = sharedProbeSurface(t, root, first, endpoint, token)
	if _, err := surface.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	stopReceipt := sharedSurfaceReceipt(t, surface, id, "Reply exactly OK. Do not call tools.", "stop-probe")
	stopTurn := stopReceipt.TurnID
	if stopTurn == "" {
		t.Fatal("dedicated stop turn omitted its native identity")
	}
	deadline = time.Now().Add(10 * time.Second)
	for !firstEvents.has("turn/started", id, stopTurn) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !firstEvents.has("turn/started", id, stopTurn) {
		t.Fatal("native start acknowledgment lacks the exact started event")
	}
	turns, err := stopClient.Rpc.Request("thread/turns/list", map[string]any{
		"threadId": id, "limit": 1, "sortDirection": "desc", "itemsView": "summary",
	})
	if err != nil {
		t.Fatalf("bounded native active-turn read failed: %v", err)
	}
	var current struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	encodedTurns, _ := json.Marshal(turns)
	if err := json.Unmarshal(encodedTurns, &current); err != nil || len(current.Data) != 1 || current.Data[0].ID != stopTurn || current.Data[0].Status != "inProgress" {
		t.Fatal("bounded native read cannot confirm the exact active turn")
	}
	err = surface.Stop(context.Background(), id, stopTurn)
	if err != nil {
		t.Fatalf("native exact-turn interrupt rejected; no retry: %v (terminal status=%s)", err, firstEvents.turnStatus(stopTurn))
	}
	deadline = time.Now().Add(20 * time.Second)
	for !firstEvents.has("turn/completed", id, stopTurn) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !firstEvents.has("turn/completed", id, stopTurn) {
		t.Fatal("native interrupt acknowledgment lacks the exact terminal event")
	}
	if firstEvents.turnStatus(stopTurn) != "interrupted" {
		t.Fatalf("Stop did not reach interrupted: %s", firstEvents.turnStatus(stopTurn))
	}
	t.Log("exact-turn interrupt reached interrupted on the other subscriber")
	conversation, err := surface.Read(context.Background(), id, "", false, nil)
	if err != nil || conversation.Runtime == nil || conversation.Runtime.ActiveTurnID != nil {
		t.Fatalf("production shared read failed: %v", err)
	}
	closeLease()
	page, err = surface.List(context.Background(), "")
	if err != nil || len(page.Sessions) != 0 {
		t.Fatalf("exited launcher remained in active catalog: %v", err)
	}
	if _, err := surface.Send(context.Background(), id, "must not send", "stale"); err == nil {
		t.Fatal("expired launcher still accepted input")
	}
	if _, err := first.Rpc.Request("thread/loaded/list", map[string]any{}); err != nil {
		t.Fatal("gateway/launcher close terminated native owner")
	}
}

type sharedProbeEvents struct {
	mu      sync.Mutex
	packets []map[string]any
}

func (e *sharedProbeEvents) receive(packet map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.packets) < 512 {
		e.packets = append(e.packets, packet)
	}
}

func (e *sharedProbeEvents) has(method, id, turn string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, packet := range e.packets {
		fields := sharedProbeObject(packet["params"])
		if packet["method"] == method && fields["threadId"] == id &&
			(turn == "" || sharedProbeObject(fields["turn"])["id"] == turn) {
			return true
		}
	}
	return false
}

func (e *sharedProbeEvents) settingsApplied(id, effort string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, packet := range e.packets {
		fields := sharedProbeObject(packet["params"])
		if packet["method"] == "thread/settings/updated" && fields["threadId"] == id &&
			sharedProbeObject(fields["threadSettings"])["effort"] == effort {
			return true
		}
	}
	return false
}

func (e *sharedProbeEvents) receipt(id, turn, client string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, packet := range e.packets {
		fields := sharedProbeObject(packet["params"])
		item := sharedProbeObject(fields["item"])
		if packet["method"] == "item/completed" && fields["threadId"] == id && fields["turnId"] == turn &&
			item["type"] == "userMessage" && item["clientId"] == client && item["id"] != "" {
			return true
		}
	}
	return false
}

func (e *sharedProbeEvents) turnStatus(turn string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, packet := range e.packets {
		fields := sharedProbeObject(packet["params"])
		value := sharedProbeObject(fields["turn"])
		if packet["method"] == "turn/completed" && value["id"] == turn {
			status, _ := value["status"].(string)
			return status
		}
	}
	return "unknown"
}

func (e *sharedProbeEvents) assistantText(turn string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, packet := range e.packets {
		fields := sharedProbeObject(packet["params"])
		item := sharedProbeObject(fields["item"])
		if packet["method"] == "item/completed" && fields["turnId"] == turn && item["type"] == "agentMessage" {
			text, _ := item["text"].(string)
			if len(text) > 200 {
				text = text[:200]
			}
			return text
		}
	}
	return "<no completed assistant item>"
}

func (e *sharedProbeEvents) assistantOK(turn string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, packet := range e.packets {
		fields := sharedProbeObject(packet["params"])
		item := sharedProbeObject(fields["item"])
		if packet["method"] == "item/completed" && fields["turnId"] == turn &&
			item["type"] == "agentMessage" && item["text"] == "OK" {
			return true
		}
	}
	return false
}

func sharedProbeObject(value any) map[string]any {
	data, _ := json.Marshal(value)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	return fields
}

func sharedProbeContains(value any, id string) bool {
	data, _ := json.Marshal(value)
	var ids []string
	_ = json.Unmarshal(data, &ids)
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
