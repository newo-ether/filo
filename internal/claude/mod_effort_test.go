package claude

import (
	"net/http"
	"testing"
)

func TestModEffortSnapshotPreservesNativeReadingAndClearsUnknown(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	sequence := uint64(0)
	for _, value := range []string{"low", "medium", "high", "xhigh", "max", "0", "8192", "9007199254740991"} {
		sequence++
		status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: sequence,
			Events: []modEvent{{Kind: "snapshot", Effort: &value}}})
		view := readRuntimeFixture(t, a)
		if status != http.StatusOK || !view.Effort.Known || view.Effort.Value == nil || *view.Effort.Value != value {
			t.Fatalf("effort=%+v status=%d", view.Effort, status)
		}
		*view.Effort.Value = "caller-mutation"
		if *readRuntimeFixture(t, a).Effort.Value != value {
			t.Fatal("effort aliases mutable registration state")
		}
	}
	sequence++
	bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: sequence,
		Events: []modEvent{{Kind: "snapshot"}}})
	if readRuntimeFixture(t, a).Effort.Known {
		t.Fatal("unknown native effort retained an old request reading")
	}
}

func TestModEffortInvalidSnapshotCannotPartiallyCommit(t *testing.T) {
	for _, value := range []string{"", "HIGH", " high", "high\n", "-1", "1.5", "01", "9007199254740992", "unknown"} {
		t.Run(value, func(t *testing.T) {
			a := fixtureAdapter(t)
			nonce := registerFixture(t, a, fixtureSession)
			initial := "high"
			bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
				Events: []modEvent{{Kind: "snapshot", Effort: &initial}}})
			status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
				Events: []modEvent{{Kind: "start", TurnID: "must-not-start"}, {Kind: "snapshot", Effort: &value}}})
			view := readRuntimeFixture(t, a)
			if status != http.StatusConflict || view.ActiveTurnID != nil || *view.Effort.Value != initial {
				t.Fatal("invalid effort partially committed a native event batch")
			}
		})
	}
	value := "high"
	if validateEvent(modEvent{Kind: "busy", Effort: &value}) == nil {
		t.Fatal("effort accepted outside a runtime snapshot")
	}
}
