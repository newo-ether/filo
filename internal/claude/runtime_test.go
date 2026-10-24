package claude

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func runtimeSnapshot(model string, tokens, window float64) modEvent {
	return modEvent{Kind: "snapshot", Model: &model, ContextTokens: &tokens, ContextWindow: &window}
}

func readRuntimeFixture(t *testing.T, a *Adapter) *protocol.Runtime {
	t.Helper()
	page, err := a.Read(context.Background(), fixtureSession, "", false, nil)
	if err != nil || page.Runtime == nil {
		t.Fatalf("runtime unavailable: %+v %v", page, err)
	}
	return page.Runtime
}

func TestRuntimeSnapshotProjectsKnownUnknownAndZeroCounts(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	push := func(sequence uint64, event modEvent) {
		t.Helper()
		status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce,
			Sequence: sequence, Events: []modEvent{event}})
		if status != http.StatusOK {
			t.Fatalf("snapshot rejected: %d", status)
		}
	}
	push(1, runtimeSnapshot("native-model", 1234, 200000))
	view := readRuntimeFixture(t, a)
	if view.Model == nil || *view.Model != "native-model" || view.ContextTokens == nil ||
		*view.ContextTokens != 1234 || view.ContextWindow == nil || *view.ContextWindow != 200000 || view.Status != "unknown" {
		t.Fatalf("native runtime snapshot changed: %+v", view)
	}
	*view.Model, *view.ContextTokens, *view.ContextWindow = "client-mutation", 99, 100
	view = readRuntimeFixture(t, a)
	if *view.Model != "native-model" || *view.ContextTokens != 1234 || *view.ContextWindow != 200000 {
		t.Fatal("returned runtime aliases registration state")
	}
	event := runtimeSnapshot("next-model", 0, 1000000)
	event.ContextTokens = nil
	push(2, event)
	view = readRuntimeFixture(t, a)
	if view.Model == nil || *view.Model != "next-model" || view.ContextTokens != nil ||
		view.ContextWindow == nil || *view.ContextWindow != 1000000 {
		t.Fatal("unknown token count retained an earlier reading")
	}
	push(3, runtimeSnapshot("next-model", 0, 1000000))
	if view = readRuntimeFixture(t, a); view.ContextTokens == nil || *view.ContextTokens != 0 {
		t.Fatal("native zero count was treated as unknown")
	}
	push(4, modEvent{Kind: "snapshot"})
	view = readRuntimeFixture(t, a)
	if view.Model != nil || view.ContextTokens != nil || view.ContextWindow != nil {
		t.Fatal("unknown snapshot retained stale native values")
	}
}

func TestInvalidRuntimeSnapshotRejectsWholeEventBatch(t *testing.T) {
	negative, fractional, unsafe, zero := -1.0, 1.5, 9007199254740992.0, 0.0
	empty, spaced, control, long := "", " native-model", "native\nmodel", strings.Repeat("m", 257)
	for name, event := range map[string]modEvent{
		"negative tokens":     {Kind: "snapshot", ContextTokens: &negative},
		"fractional tokens":   {Kind: "snapshot", ContextTokens: &fractional},
		"unsafe tokens":       {Kind: "snapshot", ContextTokens: &unsafe},
		"negative window":     {Kind: "snapshot", ContextWindow: &negative},
		"fractional window":   {Kind: "snapshot", ContextWindow: &fractional},
		"unsafe window":       {Kind: "snapshot", ContextWindow: &unsafe},
		"zero window":         {Kind: "snapshot", ContextWindow: &zero},
		"empty model":         {Kind: "snapshot", Model: &empty},
		"spaced model":        {Kind: "snapshot", Model: &spaced},
		"control model":       {Kind: "snapshot", Model: &control},
		"oversize model":      {Kind: "snapshot", Model: &long},
		"row on snapshot":     {Kind: "snapshot", UUID: fixtureRow},
		"turn on snapshot":    {Kind: "snapshot", TurnID: "native-turn"},
		"model outside event": {Kind: "busy", Model: &control},
	} {
		t.Run(name, func(t *testing.T) {
			a := fixtureAdapter(t)
			nonce := registerFixture(t, a, fixtureSession)
			bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
				Events: []modEvent{runtimeSnapshot("original-model", 12, 100)}})
			status, response := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
				Events: []modEvent{{Kind: "start", TurnID: "must-not-start"}, runtimeSnapshot("must-not-apply", 42, 100), event}})
			view := readRuntimeFixture(t, a)
			if status != http.StatusConflict || response.Sequence != 1 || view.ActiveTurnID != nil ||
				view.Model == nil || *view.Model != "original-model" || *view.ContextTokens != 12 {
				t.Fatal("invalid snapshot partially changed the batch or its sequence")
			}
		})
	}
}

func TestRuntimeSnapshotRollsBackWhenNativeRowConflicts(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
		Events: []modEvent{runtimeSnapshot("original-model", 12, 100),
			{Kind: "row", UUID: fixtureRow, Role: "assistant", Text: "original"}}})
	status, response := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
		Events: []modEvent{runtimeSnapshot("must-not-apply", 42, 100),
			{Kind: "row", UUID: fixtureRow, Role: "assistant", Text: "conflicting"}}})
	view := readRuntimeFixture(t, a)
	if status != http.StatusConflict || response.Sequence != 1 || view.Model == nil || *view.Model != "original-model" {
		t.Fatal("row conflict partially committed a new snapshot")
	}
}

func TestRuntimeSnapshotCannotSurviveRegistrationLifecycle(t *testing.T) {
	for _, transition := range []string{"register", "revoke", "expiry"} {
		t.Run(transition, func(t *testing.T) {
			a := fixtureAdapter(t)
			now := time.Unix(1000, 0)
			a.now = func() time.Time { return now }
			nonce := registerFixture(t, a, fixtureSession)
			bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
				Events: []modEvent{runtimeSnapshot("old-registration-model", 12, 100)}})
			if transition == "revoke" {
				bridgeCall(t, a, "revoke", envelope{SessionID: fixtureSession, Nonce: nonce})
			}
			if transition == "expiry" {
				now = now.Add(DefaultTTL)
			}
			if transition != "register" {
				if _, err := a.Read(context.Background(), fixtureSession, "", false, nil); err == nil {
					t.Fatal("retired registration remained readable")
				}
			}
			registerFixture(t, a, fixtureSession)
			view := readRuntimeFixture(t, a)
			if view.Model != nil || view.ContextTokens != nil || view.ContextWindow != nil {
				t.Fatal("new registration reused a retired runtime snapshot")
			}
			status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
				Events: []modEvent{runtimeSnapshot("stale-model", 42, 100)}})
			if status != http.StatusConflict {
				t.Fatal("retired registration supplied a new snapshot")
			}
		})
	}
}
