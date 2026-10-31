package service

import (
	"context"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

type cliControlledRuntimeHistory struct {
	TaskSessionsHistory
	runtime *protocol.Runtime
}

func (h *cliControlledRuntimeHistory) Read(ctx context.Context, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
	page, err := h.TaskSessionsHistory.Read(ctx, id, cursor, activity, excluded)
	page.Runtime = h.runtime
	return page, err
}

func TestCLIControlledRetiredIdleResendRebindsWithoutReplaying(t *testing.T) {
	surface, fixture := newCLIControlledFixture(t)
	ctx := context.Background()
	if _, _, err := surface.TaskSessions.Create(ctx, "hello", "client", protocol.SessionSettings{}); err != nil {
		t.Fatal(err)
	}
	live, err := surface.Access(ctx, taskSessionsTaskID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.peer.finish()
	fixture.rpc.Close()
	fixture.sessions.mu.Lock()
	delete(fixture.sessions.entries, taskSessionsTaskID)
	fixture.sessions.mu.Unlock()
	history := &cliControlledRuntimeHistory{TaskSessionsHistory: fixture.history,
		runtime: &protocol.Runtime{Status: "notLoaded", CompletedTurnID: protocol.Known(pointerTo(taskSessionsTurnID))}}
	surface.TaskSessions.history = history
	idle, err := surface.Access(ctx, taskSessionsTaskID)
	if err != nil || idle.Capabilities.Live || !idle.Capabilities.IdleSend || idle.Binding == live.Binding {
		t.Fatalf("retired access=%+v err=%v", idle, err)
	}
	if _, err := surface.Send(protocol.WithAccessBinding(ctx, live.Binding), taskSessionsTaskID, "stale", "stale"); err == nil {
		t.Fatal("retired executor accepted its old live binding")
	}
	if err := surface.Stop(protocol.WithAccessBinding(ctx, idle.Binding), taskSessionsTaskID, taskSessionsTurnID); err == nil {
		t.Fatal("stop reopened a retired executor")
	}
	if opens, _ := fixture.factory.counts(); opens != 1 {
		t.Fatalf("rejected mutations opened writers: %d", opens)
	}
	rpc, peer := newTaskSessionsPeer()
	peer.mu.Lock()
	peer.owned, peer.finished = true, true
	peer.mu.Unlock()
	fixture.factory.connection = &taskSessionsTestConnection{rpc: rpc}
	t.Cleanup(rpc.Close)
	receipt, err := surface.Send(protocol.WithAccessBinding(ctx, idle.Binding), taskSessionsTaskID, "again", "second")
	if err != nil || receipt.TurnID != taskSessionsTurnID {
		t.Fatalf("idle resend receipt=%+v err=%v", receipt, err)
	}
	rebound, err := surface.Access(ctx, taskSessionsTaskID)
	if err != nil || !rebound.Capabilities.Live || rebound.Binding == idle.Binding {
		t.Fatalf("resumed access=%+v err=%v", rebound, err)
	}
	if _, err := surface.Send(protocol.WithAccessBinding(ctx, idle.Binding), taskSessionsTaskID, "stale", "stale"); err == nil {
		t.Fatal("new executor accepted an expired dormant binding")
	}
	if peer.callCount("turn/start") != 1 || peer.callCount("thread/start") != 0 || peer.callCount("turn/steer") != 0 {
		t.Fatal("resume replayed creation/input or dispatched stale binding")
	}
}

func TestCLIControlledDormantUnknownAndActiveNeverAcquireWriter(t *testing.T) {
	for _, runtime := range []*protocol.Runtime{nil, {Status: "unknown"}, {Status: "notLoaded"},
		{Status: "active", ActiveTurnID: pointerTo(taskSessionsTurnID)}} {
		surface, fixture := newCLIControlledFixture(t)
		surface.TaskSessions.history = &cliControlledRuntimeHistory{TaskSessionsHistory: fixture.history, runtime: runtime}
		access, err := surface.Access(context.Background(), taskSessionsTaskID)
		if err != nil || access.Capabilities.IdleSend || access.Capabilities.Stop || access.Capabilities.Live {
			t.Fatalf("runtime=%+v access=%+v err=%v", runtime, access, err)
		}
		if _, err := surface.Send(context.Background(), taskSessionsTaskID, "no", "no"); err == nil {
			t.Fatalf("runtime=%+v admitted mutation", runtime)
		}
		if opens, _ := fixture.factory.counts(); opens != 0 {
			t.Fatal("unknown/active persisted history acquired a writer")
		}
	}
}

func TestCLIControlledScopeChangeInvalidatesLiveAccess(t *testing.T) {
	surface, fixture := newCLIControlledFixture(t)
	ctx := context.Background()
	if _, _, err := surface.TaskSessions.Create(ctx, "hello", "client", protocol.SessionSettings{}); err != nil {
		t.Fatal(err)
	}
	access, err := surface.Access(ctx, taskSessionsTaskID)
	if err != nil {
		t.Fatal(err)
	}
	surface.scope = func(context.Context) (string, error) { return "different-native-account", nil }
	if _, err := surface.Send(protocol.WithAccessBinding(ctx, access.Binding), taskSessionsTaskID, "no", "no"); err == nil {
		t.Fatal("scope change retained previous mutation authority")
	}
	if fixture.peer.callCount("turn/steer") != 0 {
		t.Fatal("changed scope dispatched native input")
	}
}

func TestCLIControlledCloseNeverInterruptsAcceptedTurn(t *testing.T) {
	surface, fixture := newCLIControlledFixture(t)
	if _, _, err := surface.TaskSessions.Create(context.Background(), "hello", "client", protocol.SessionSettings{}); err != nil {
		t.Fatal(err)
	}
	surface.Close()
	if fixture.peer.callCount("turn/interrupt") != 0 || !fixture.peer.isActive() {
		t.Fatal("closing the adapter interrupted accepted native work")
	}
	if _, err := surface.Send(context.Background(), taskSessionsTaskID, "no", "no"); err == nil {
		t.Fatal("closed adapter admitted new input")
	}
}
