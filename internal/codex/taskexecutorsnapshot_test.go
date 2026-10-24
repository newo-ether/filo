package codex

import (
	"testing"

	"github.com/newo-ether/filo/internal/nativejson"
)

func TestTaskExecutorSnapshotDoesNotReadAnUnpublishedRollout(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "active"
		if completed {
			name = "completed-before-receipt"
		}
		t.Run(name, func(t *testing.T) {
			f := newExecutorFixture(t, executorFixtureOptions{finishBeforeReceipt: completed})
			session := NewNativeTaskSession(f.client.Rpc)
			created, err := session.Create()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := session.StartTurn(created.ID, nil, "client"); err != nil {
				t.Fatal(err)
			}
			snapshot, err := session.Snapshot(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			runtime := snapshot.Page.Runtime
			if runtime == nil || snapshot.TurnID == nil || *snapshot.TurnID != "turn" || !snapshot.Materialized {
				t.Fatalf("snapshot = %+v", snapshot)
			}
			if completed {
				if runtime.Status != "idle" || runtime.ActiveTurnID != nil || !runtime.CompletedTurnID.Known ||
					runtime.CompletedTurnID.Value == nil || *runtime.CompletedTurnID.Value != "turn" {
					t.Fatalf("terminal runtime = %+v", runtime)
				}
			} else if runtime.Status != "active" || runtime.ActiveTurnID == nil || *runtime.ActiveTurnID != "turn" {
				t.Fatalf("active runtime = %+v", runtime)
			}
			if f.countMethod("thread/read") != 0 || f.countMethod("thread/start") != 1 || f.countMethod("turn/start") != 1 {
				t.Fatal("snapshot queried persistence or replayed input", f.callMethods())
			}
		})
	}
}

func TestTaskExecutorSnapshotRetainsFailedTurnIdentity(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{})
	session := NewNativeTaskSession(f.client.Rpc)
	created, err := session.Create()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.StartTurn(created.ID, nil, "client"); err != nil {
		t.Fatal(err)
	}
	f.failTurn()
	snapshot, err := session.Snapshot(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtime := snapshot.Page.Runtime
	if runtime.Status != "systemError" || !runtime.CompletedTurnID.Known || runtime.CompletedTurnID.Value == nil ||
		*runtime.CompletedTurnID.Value != "turn" {
		t.Fatalf("failed runtime = %+v", runtime)
	}
	if _, err := f.client.Rpc.Request("filo/task/state", map[string]any{"threadId": "foreign"}); err == nil {
		t.Fatal("foreign session obtained live state")
	}
	state, err := f.client.Rpc.Request("filo/task/state", map[string]any{"threadId": created.ID})
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := nativejson.Fields(state)
	thread, _ := nativejson.Fields(fields["thread"])
	if thread["id"] != created.ID {
		t.Fatal("executor did not retain the exact native identity")
	}
}

func TestTaskExecutorClosedThreadDoesNotRetainLiveAccessIdentity(t *testing.T) {
	for _, method := range []string{"thread/closed", "thread/archived"} {
		t.Run(method, func(t *testing.T) {
			f := newExecutorFixture(t, executorFixtureOptions{})
			if _, err := f.client.Rpc.Request("thread/start", map[string]any{}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.client.Rpc.Request("turn/start", map[string]any{"threadId": executorTaskID}); err != nil {
				t.Fatal(err)
			}
			f.executor.handleNotification(map[string]any{"method": method,
				"params": map[string]any{"threadId": executorTaskID}})
			state, err := f.client.Rpc.Request("filo/task/state", map[string]any{"threadId": executorTaskID})
			if err != nil {
				t.Fatal(err)
			}
			fields, _ := nativejson.Fields(state)
			if _, present := fields["thread"]; present {
				t.Fatal("closed native thread retained live access identity")
			}
		})
	}
}
