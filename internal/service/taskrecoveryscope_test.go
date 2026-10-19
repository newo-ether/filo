package service

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/sessions"
)

func TestTaskExecutorFactoryRecoveryScopeNeverGrantsBrowsingAllocation(t *testing.T) {
	f := newExecutorFactoryFixture(t)
	factory := factoryFor(t, f, 4, time.Second)
	defer factory.Close()
	id := "11111111-1111-4111-8111-111111111111"
	ctx := sessions.WithIdleRecovery(context.Background(), id)
	if conn, err := factory.Follow(ctx, id); err != nil || conn != nil {
		t.Fatal(conn, err)
	}
	if _, err := os.Stat(filepath.Join(f.directory, "tasks", id)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("browsing wrote scope", err)
	}
	if _, err := factory.Open(context.Background(), &id); err == nil {
		t.Fatal("unadmitted task opened")
	}
	conn, err := factory.Open(ctx, &id)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	reconnected, err := factory.Open(context.Background(), &id)
	if err != nil || reconnected == nil || f.runningCount() != 1 {
		t.Fatal("existing recovery required idle admission", reconnected, err)
	}
	reconnected.Close()
	if created, err := factory.Created(id); err != nil || created {
		t.Fatal("recovery fabricated creation", created, err)
	}
	if owned, err := factory.Owns(id); err != nil || !owned {
		t.Fatal("recovery cannot be followed", owned, err)
	}
	// Exercise recovery-reference fallback without a current pointer.
	if err := os.Remove(filepath.Join(f.directory, "tasks", id, executorCurrentName)); err != nil {
		t.Fatal(err)
	}
	followed, err := factory.Follow(context.Background(), id)
	if err != nil || followed == nil || f.runningCount() != 1 {
		t.Fatal("follow allocated or missed recovered executor", followed, err)
	}
	followed.Close()
	if _, err := f.runningExecutor(0).core.Retire(); err != nil {
		t.Fatal(err)
	}
	if followed, err = factory.Follow(ctx, id); err != nil || followed != nil || f.runningCount() != 1 {
		t.Fatal("retired follow allocated", followed, err)
	}
	if _, err := factory.Open(context.Background(), &id); err == nil || f.runningCount() != 1 {
		t.Fatal("recovery reference granted reallocation", err)
	}
	other := sessions.WithIdleRecovery(context.Background(), "22222222-2222-4222-8222-222222222222")
	if _, err := factory.Open(other, &id); err == nil {
		t.Fatal("cross-task admission")
	}
	entry := f.runningExecutor(0)
	f.mutate(entry, func(state *ExecutorState) { state.Phase, state.URL = "disconnected", nil })
	if err := SaveExecutorState(entry.directory, f.snapshot(entry)); err != nil {
		t.Fatal(err)
	}
	if _, err := factory.Open(context.Background(), &id); err == nil || f.runningCount() != 1 {
		t.Fatal("unknown readiness allocated a replacement", err)
	}
	f.mutate(entry, func(state *ExecutorState) { state.Phase = "retired" })
	if err := SaveExecutorState(entry.directory, f.snapshot(entry)); err != nil {
		t.Fatal(err)
	}
	conn, err = factory.Open(ctx, &id)
	if err != nil || f.runningCount() != 2 {
		t.Fatal("explicit recovery failed", err)
	}
	conn.Close()
}

func TestCreatedTaskClientRecoveryIsExplicitAndPreservesContext(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	calls := 0
	client := NewCreatedTaskClient(t.TempDir(), func(ctx context.Context, path string, input any) (json.RawMessage, error) {
		calls++
		if !sessions.IdleRecoveryAllowed(ctx, id) {
			t.Error("admission lost")
		}
		if strings.HasSuffix(path, "/settings") {
			return json.RawMessage(`{"updated":true}`), nil
		}
		return json.RawMessage(`{"turnId":"next","clientId":"client"}`), nil
	}, nil, nil)
	defer client.Close()
	ctx := sessions.WithIdleRecovery(context.Background(), id)
	if _, err := client.Send(context.Background(), id, "hello", "client"); err == nil {
		t.Fatal("missing admission accepted")
	}
	if _, err := client.Send(ctx, id, "hello", "client"); err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateSettings(ctx, id, protocol.SessionSettings{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(ctx, id, nil, false); err == nil {
		t.Fatal("read gained mutation admission")
	}
	if err := client.Stop(ctx, id, "turn"); err == nil {
		t.Fatal("stop gained mutation admission")
	}
	if calls != 2 {
		t.Fatal("mutation replayed", calls)
	}
}

func TestCreatedTaskClientRecoveredSendReachesScopedNativeSteerWithoutIdleHeader(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	fixture.peer.mu.Lock()
	fixture.peer.owned, fixture.peer.active = true, true
	fixture.peer.mu.Unlock()
	directory := t.TempDir()
	taskDirectory := filepath.Join(directory, "tasks", taskSessionsTaskID)
	if err := os.MkdirAll(taskDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveExecutorFile(taskDirectory, executorRecoveryName,
		map[string]string{"executorId": "33333333-3333-4333-8333-333333333333"}); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("X-Filo-Idle-Recovery") != "" {
			t.Error("existing recovered send lost authentication or forged idle admission")
		}
		var input struct{ Text, ClientID string }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx := (&Server{layer: "worker"}).mutationContext(r, taskSessionsTaskID, "messages")
		if sessions.IdleRecoveryAllowed(ctx, taskSessionsTaskID) {
			t.Error("active send gained idle recovery at helper")
		}
		receipt, err := fixture.sessions.Send(ctx, taskSessionsTaskID, input.Text, input.ClientID)
		if err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(receipt)
	}))
	defer server.Close()
	worker, err := NewUserWorker(server.URL, StaticUserWorkerToken(strings.Repeat("a", 64)),
		userWorkerUnusedHistory{}, directory, UserWorkerTimeouts{Read: time.Second, Mutation: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	receipt, err := worker.NewTasks().Send(context.Background(), taskSessionsTaskID, "adjust", "client")
	if err != nil || receipt.TurnID != taskSessionsTurnID || receipt.ClientID != "client" {
		t.Fatal(receipt, err)
	}
	if requests != 1 || fixture.peer.callCount("turn/steer") != 1 || fixture.peer.callCount("turn/start") != 0 || len(fixture.history.history()) != 0 {
		t.Fatal("active send replayed, started a turn or required idle history", requests, fixture.peer.calls())
	}
	params := fixture.peer.callsFor("turn/steer")[0].params
	if params["expectedTurnId"] != taskSessionsTurnID || params["clientUserMessageId"] != "client" {
		t.Fatal("steer changed native input identity", params)
	}
}

func TestUserWorkerRecoveryHeaderIsExactAndMutationOnly(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	other := "22222222-2222-4222-8222-222222222222"
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("X-Filo-Idle-Recovery")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	worker, err := NewUserWorker(server.URL, StaticUserWorkerToken(strings.Repeat("a", 64)),
		userWorkerUnusedHistory{}, "", UserWorkerTimeouts{Read: time.Second, Mutation: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	ctx := sessions.WithIdleRecovery(context.Background(), id)
	for _, test := range []struct {
		path     string
		input    any
		expected string
	}{
		{"/v1/sessions/" + id + "/messages", map[string]any{}, id},
		{"/v1/sessions/" + id + "/settings", map[string]any{}, id},
		{"/v1/sessions/" + id + "/model", map[string]any{}, id},
		{"/v1/sessions/" + other + "/messages", map[string]any{}, ""},
		{"/v1/sessions/" + id + "/stop", map[string]any{}, ""},
		{"/v1/sessions/" + id, nil, ""},
	} {
		if _, err := worker.call(ctx, test.path, test.input); err != nil {
			t.Fatal(err)
		}
		if header := <-seen; header != test.expected {
			t.Fatalf("%s header=%q", test.path, header)
		}
	}
}
