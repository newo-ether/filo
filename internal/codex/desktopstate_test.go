package codex

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func desktopFixture() map[string]any {
	return map[string]any{
		"id": "thread", "hostId": "local", "cwd": "C:/project", "turns": []any{},
		"threadRuntimeStatus": map[string]any{"type": "active"},
		"latestModel":         "model",
		"latestTokenUsageInfo": map[string]any{
			"last":               map[string]any{"totalTokens": float64(41)},
			"modelContextWindow": float64(1000),
		},
		"turnHistory": map[string]any{
			"kind": "canonical",
			"history": map[string]any{
				"islands": []any{map[string]any{
					"entries": []any{
						map[string]any{"value": "a"},
						map[string]any{"value": "b"},
					},
				}},
				"entitiesByKey": map[string]any{
					"a": map[string]any{
						"turnId": "old", "status": "completed", "turnStartedAtMs": float64(1000),
						"items": []any{
							map[string]any{"type": "userMessage", "id": "u1",
								"content": []any{map[string]any{"type": "text", "text": "first"}}},
							map[string]any{"type": "agentMessage", "id": "a1", "text": "answer"},
						},
					},
					"b": map[string]any{
						"turnId": "live", "status": "inProgress", "turnStartedAtMs": float64(2000),
						"items": []any{
							map[string]any{"type": "userMessage", "id": "u2", "clientId": "client",
								"content": []any{map[string]any{"type": "text", "text": "second"}}},
							map[string]any{"type": "agentMessage", "id": "a2", "text": "stream"},
							map[string]any{"type": "reasoning", "id": "r",
								"summary": []any{"public"}, "content": []any{"hidden"}},
						},
					},
				},
			},
		},
	}
}

func entityOf(t *testing.T, state map[string]any, key string) map[string]any {
	t.Helper()
	history, _ := state["turnHistory"].(map[string]any)
	inner, _ := history["history"].(map[string]any)
	entities, _ := inner["entitiesByKey"].(map[string]any)
	entity, _ := entities[key].(map[string]any)
	if entity == nil {
		t.Fatalf("missing entity %q", key)
	}
	return entity
}

func mustStatusString(t *testing.T, p *string) string {
	t.Helper()
	if p == nil {
		t.Fatalf("status is nil")
	}
	return *p
}

func mustString(t *testing.T, p *string) string {
	t.Helper()
	if p == nil {
		t.Fatalf("expected string, got nil")
	}
	return *p
}

// Mirrors: canonical history retains order, identity, native runtime and
// only public activity.
func TestCanonicalHistoryOrderIdentityAndActivity(t *testing.T) {
	page, err := ProjectDesktop(desktopFixture(), true, 0)
	if err != nil {
		t.Fatalf("ProjectDesktop: %v", err)
	}
	wantIDs := []string{"u1", "a1", "u2", "a2", "r"}
	if len(page.Messages) != len(wantIDs) {
		t.Fatalf("messages = %d, want %d", len(page.Messages), len(wantIDs))
	}
	for i, want := range wantIDs {
		if page.Messages[i].ID != want {
			t.Errorf("messages[%d].id = %q, want %q", i, page.Messages[i].ID, want)
		}
	}
	if got := mustString(t, page.Messages[2].ClientID); got != "client" {
		t.Errorf("messages[2].clientId = %q, want client", got)
	}
	if got := mustString(t, page.Runtime.ActiveTurnID); got != "live" {
		t.Errorf("activeTurnId = %q, want live", got)
	}
	if page.Runtime.ContextTokens == nil || *page.Runtime.ContextTokens != 41 {
		t.Errorf("contextTokens = %v, want 41", page.Runtime.ContextTokens)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "hidden") {
		t.Errorf("projection leaked private reasoning content: %s", encoded)
	}
	quiet, err := ProjectDesktop(desktopFixture(), false, 0)
	if err != nil {
		t.Fatalf("ProjectDesktop: %v", err)
	}
	if len(quiet.Messages) != 4 {
		t.Errorf("without activity: %d messages, want 4", len(quiet.Messages))
	}
}

// Mirrors: patches update streamed text atomically without mutating earlier
// snapshots.
func TestPatchesAtomicWithStructuralSharing(t *testing.T) {
	original := desktopFixture()
	result, err := ApplyDesktopPatches(original, []any{
		map[string]any{"op": "replace",
			"path":  []any{"turnHistory", "history", "entitiesByKey", "b", "items", float64(1), "text"},
			"value": "complete"},
		map[string]any{"op": "replace",
			"path": []any{"threadRuntimeStatus", "type"}, "value": "idle"},
		map[string]any{"op": "replace",
			"path":  []any{"turnHistory", "history", "entitiesByKey", "b", "status"},
			"value": "completed"},
	})
	if err != nil {
		t.Fatalf("ApplyDesktopPatches: %v", err)
	}
	originalPage, err := ProjectDesktop(original, false, 0)
	if err != nil {
		t.Fatalf("ProjectDesktop(original): %v", err)
	}
	if got := originalPage.Messages[len(originalPage.Messages)-1].Text; got != "stream" {
		t.Errorf("original last text = %q, want stream", got)
	}
	resultPage, err := ProjectDesktop(result.(map[string]any), false, 0)
	if err != nil {
		t.Fatalf("ProjectDesktop(result): %v", err)
	}
	if got := resultPage.Messages[len(resultPage.Messages)-1].Text; got != "complete" {
		t.Errorf("result last text = %q, want complete", got)
	}
	if resultPage.Runtime.ActiveTurnID != nil {
		t.Errorf("result activeTurnId = %q, want null", *resultPage.Runtime.ActiveTurnID)
	}
	// Untouched island entries keep pointer identity (structural sharing).
	before := entityOf(t, original, "a")
	after := entityOf(t, result.(map[string]any), "a")
	if reflect.ValueOf(before).Pointer() != reflect.ValueOf(after).Pointer() {
		t.Errorf("entity a was copied; structural sharing broken")
	}
}

// Mirrors: bad patches and prototype pollution leave original data intact.
func TestBadPatchesLeaveOriginalIntact(t *testing.T) {
	original := desktopFixture()
	before, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	paths := [][]any{
		{"__proto__", "polluted"},
		{"missing", "value"},
		{"id"},
	}
	for _, path := range paths {
		if _, err := ApplyDesktopPatches(original, []any{map[string]any{
			"op": "replace", "path": path, "value": "wrong",
		}}); err == nil {
			t.Errorf("patch path %v did not throw", path)
		}
	}
	after, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("original mutated by failed patches")
	}
	if _, polluted := original["polluted"]; polluted {
		t.Errorf("prototype pollution reached the state object")
	}
}

// Mirrors: only accepted steering input appears, with native client and turn
// identities.
func TestOnlyAcceptedSteeringInputAppears(t *testing.T) {
	state := desktopFixture()
	b := entityOf(t, state, "b")
	items, _ := b["items"].([]any)
	items = append(items,
		map[string]any{"type": "steeringUserMessage", "id": "pending", "status": "pending", "input": []any{}},
		map[string]any{"type": "steeringUserMessage", "id": "local",
			"serverUserMessageId": "native", "clientUserMessageId": "steer",
			"status": "accepted",
			"input":  []any{map[string]any{"type": "text", "text": "follow up"}}},
		map[string]any{"type": "steered", "id": "native"},
	)
	b["items"] = items
	page, err := ProjectDesktop(state, false, 0)
	if err != nil {
		t.Fatalf("ProjectDesktop: %v", err)
	}
	last := page.Messages[len(page.Messages)-1]
	if last.ID != "native" {
		t.Errorf("last id = %q, want native", last.ID)
	}
	if last.TurnID != "live" {
		t.Errorf("last turnId = %q, want live", last.TurnID)
	}
	if got := mustString(t, last.ClientID); got != "steer" {
		t.Errorf("last clientId = %q, want steer", got)
	}
	for _, message := range page.Messages {
		if message.ID == "pending" {
			t.Errorf("pending steering leaked into projection")
		}
	}
}

// Mirrors: authoritative idle or unavailable runtime clears stale active
// identity without changing history.
func TestAuthoritativeRuntimeClearsStaleActive(t *testing.T) {
	state := desktopFixture()
	page, err := ProjectDesktop(state, true, 0)
	if err != nil {
		t.Fatalf("ProjectDesktop: %v", err)
	}
	messages := page.Messages
	for _, typ := range []string{"idle", "notLoaded", "systemError"} {
		state["threadRuntimeStatus"] = map[string]any{"type": typ}
		status := DesktopStatus(state)
		if got := mustStatusString(t, status.Status); got != typ {
			t.Errorf("status = %q, want %q", got, typ)
		}
		if status.ActiveTurnID != nil {
			t.Errorf("activeTurnId = %q, want null", *status.ActiveTurnID)
		}
		projected, err := ProjectDesktop(state, false, 0)
		if err != nil {
			t.Fatalf("ProjectDesktop: %v", err)
		}
		if projected.Runtime.ActiveTurnID != nil {
			t.Errorf("runtime activeTurnId = %q, want null", *projected.Runtime.ActiveTurnID)
		}
		again, err := ProjectDesktop(state, true, 0)
		if err != nil {
			t.Fatalf("ProjectDesktop: %v", err)
		}
		if !reflect.DeepEqual(again.Messages, messages) {
			t.Errorf("history changed under runtime type %q", typ)
		}
	}
}

// Mirrors: older unfinished turns cannot become active after a newer
// terminal turn (canonical × plain × runtime matrix).
func TestStaleInProgressCannotBecomeActive(t *testing.T) {
	for _, canonical := range []bool{true, false} {
		for _, runtimeType := range []any{"idle", "active", nil} {
			state := desktopFixture()
			entityOf(t, state, "a")["status"] = "inProgress"
			entityOf(t, state, "b")["status"] = "completed"
			if runtimeType == nil {
				delete(state, "threadRuntimeStatus")
			} else {
				state["threadRuntimeStatus"] = map[string]any{"type": runtimeType}
			}
			if !canonical {
				state["turns"] = []any{entityOf(t, state, "a"), entityOf(t, state, "b")}
				delete(state, "turnHistory")
			}
			expected := "idle"
			if runtimeType != nil {
				expected = runtimeType.(string)
			}
			status := DesktopStatus(state)
			if got := mustStatusString(t, status.Status); got != expected {
				t.Errorf("canonical=%v type=%v: status = %q, want %q", canonical, runtimeType, got, expected)
			}
			if status.ActiveTurnID != nil {
				t.Errorf("canonical=%v type=%v: activeTurnId = %q, want null", canonical, runtimeType, *status.ActiveTurnID)
			}
			if got := mustString(t, status.CompletedTurnID); got != "live" {
				t.Errorf("canonical=%v type=%v: completedTurnId = %q, want live", canonical, runtimeType, got)
			}
			for _, limit := range []float64{0, 1} {
				projected, err := ProjectDesktop(state, true, limit)
				if err != nil {
					t.Fatalf("ProjectDesktop: %v", err)
				}
				runtime := projected.Runtime
				if runtime.Status != expected {
					t.Errorf("canonical=%v type=%v limit=%v: runtime status = %q, want %q", canonical, runtimeType, limit, runtime.Status, expected)
				}
				if runtime.ActiveTurnID != nil {
					t.Errorf("canonical=%v type=%v limit=%v: runtime activeTurnId = %q, want null", canonical, runtimeType, limit, *runtime.ActiveTurnID)
				}
				if !runtime.CompletedTurnID.Known || runtime.CompletedTurnID.Value == nil || *runtime.CompletedTurnID.Value != "live" {
					t.Errorf("canonical=%v type=%v limit=%v: runtime completedTurnId = %+v, want live", canonical, runtimeType, limit, runtime.CompletedTurnID)
				}
			}
		}
	}
}

// Mirrors: the current running tail remains active before and after
// authoritative state arrives.
func TestRunningTailStaysActive(t *testing.T) {
	state := desktopFixture()
	for _, runtime := range []any{nil, map[string]any{"type": "active"}} {
		if runtime == nil {
			delete(state, "threadRuntimeStatus")
		} else {
			state["threadRuntimeStatus"] = runtime
		}
		status := DesktopStatus(state)
		if got := mustStatusString(t, status.Status); got != "active" {
			t.Errorf("status = %q, want active", got)
		}
		if got := mustString(t, status.ActiveTurnID); got != "live" {
			t.Errorf("activeTurnId = %q, want live", got)
		}
		projected, err := ProjectDesktop(state, true, 1)
		if err != nil {
			t.Fatalf("ProjectDesktop: %v", err)
		}
		if got := mustString(t, projected.Runtime.ActiveTurnID); got != "live" {
			t.Errorf("runtime activeTurnId = %q, want live", got)
		}
	}
}

func TestNonArrayItemsRejected(t *testing.T) {
	state := desktopFixture()
	entityOf(t, state, "b")["items"] = "nope"
	if _, err := ProjectDesktop(state, false, 0); err == nil ||
		err.Error() != "Unsupported desktop item representation" {
		t.Errorf("error = %v, want Unsupported desktop item representation", err)
	}
}
