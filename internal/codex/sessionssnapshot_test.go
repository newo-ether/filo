package codex

import (
	"errors"
	"testing"
)

// Accepted native turns can precede the asynchronous rollout writer. Access
// must use the executor's same-owner live identity without replaying input.
func TestNativeTaskSnapshotUsesExecutorIdentityBeforeRolloutPublication(t *testing.T) {
	host := newSessionHost()
	host.replies["thread/read"] = sessionFailure(errors.New("thread-store internal error: rollout is empty"))
	host.replies["filo/task/state"] = sessionReply(map[string]any{
		"thread":  map[string]any{"id": "task", "model": "native-model", "status": map[string]any{"type": "active"}},
		"turn":    map[string]any{"id": "accepted-turn", "status": "inProgress"},
		"hasUser": true, "materialized": true,
	})
	snapshot, err := NewNativeTaskSession(host).Snapshot("task")
	if err != nil {
		t.Fatal(err)
	}
	runtime := snapshot.Page.Runtime
	if runtime == nil || runtime.ActiveTurnID == nil || *runtime.ActiveTurnID != "accepted-turn" ||
		runtime.Model == nil || *runtime.Model != "native-model" {
		t.Fatalf("live runtime = %+v", runtime)
	}
	if host.count("thread/read") != 0 || host.count("turn/start") != 0 || host.count("thread/start") != 0 {
		t.Fatal("snapshot read asynchronous storage or replayed input", host.methods())
	}
}

func TestNativeTaskSnapshotRejectsForeignLiveIdentityAndPreservesStorageErrors(t *testing.T) {
	for _, live := range []bool{false, true} {
		host := newSessionHost()
		storageErr := errors.New("native storage failure")
		host.replies["thread/read"] = sessionFailure(storageErr)
		state := map[string]any{"hasUser": true, "materialized": true}
		if live {
			state["thread"] = map[string]any{"id": "foreign"}
		}
		host.replies["filo/task/state"] = sessionReply(state)
		_, err := NewNativeTaskSession(host).Snapshot("task")
		if live {
			if err == nil || err.Error() != "Native task identity mismatch" || host.count("thread/read") != 0 {
				t.Fatalf("foreign live identity: err=%v calls=%v", err, host.methods())
			}
		} else if !errors.Is(err, storageErr) || host.count("thread/read") != 1 {
			t.Fatalf("storage failure hidden: err=%v calls=%v", err, host.methods())
		}
	}
}
