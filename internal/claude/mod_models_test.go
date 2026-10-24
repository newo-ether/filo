package claude

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestModModelCatalogTracksOnlyLiveRegistrationSnapshots(t *testing.T) {
	a := fixtureAdapter(t)
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	nonce := registerFixture(t, a, fixtureSession)
	push := func(sequence uint64, options []string) {
		t.Helper()
		code, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: sequence,
			Events: []modEvent{{Kind: "snapshot", ModelOptions: options}}})
		if code != http.StatusOK {
			t.Fatal("catalog snapshot rejected", code)
		}
	}
	push(1, []string{"default", "sonnet", "opus[1m]"})
	models, err := a.Models(context.Background())
	if err != nil || len(models) != 3 || models[1].ID != "sonnet" || len(models[1].ReasoningEfforts) != 5 || models[0].IsDefault {
		t.Fatalf("catalog fabricated choices/default/effort: %+v %v", models, err)
	}
	models[0].ID = "client-write"
	again, _ := a.Models(context.Background())
	if again[0].ID != "default" {
		t.Fatal("catalog leaked mutable state")
	}
	push(2, nil)
	again, _ = a.Models(context.Background())
	if len(again) != 0 {
		t.Fatal("failed native catalog read retained old models")
	}
	push(3, []string{"haiku"})
	now = now.Add(DefaultTTL)
	again, _ = a.Models(context.Background())
	if len(again) != 0 {
		t.Fatal("expired registration retained native models")
	}
}

func TestModModelCatalogRejectsInvalidBatchAtomically(t *testing.T) {
	for _, options := range [][]string{{"same", "same"}, {""}, {" spaced"}, {"bad\nmodel"}, {strings.Repeat("m", 257)}, make([]string, 33)} {
		a := fixtureAdapter(t)
		nonce := registerFixture(t, a, fixtureSession)
		bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
			Events: []modEvent{{Kind: "snapshot", ModelOptions: []string{"original"}}}})
		code, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
			Events: []modEvent{{Kind: "start", TurnID: "must-not-start"}, {Kind: "snapshot", ModelOptions: options}}})
		models, _ := a.Models(context.Background())
		if code != http.StatusConflict || len(models) != 1 || models[0].ID != "original" || readRuntimeFixture(t, a).ActiveTurnID != nil {
			t.Fatal("invalid catalog partially changed native state")
		}
	}
	if err := validateEvent(modEvent{Kind: "busy", ModelOptions: []string{"bad-placement"}}); err == nil {
		t.Fatal("catalog accepted outside native snapshot")
	}
	if !reflect.DeepEqual(Capabilities().Settings, []string{"model", "effort"}) {
		t.Fatal("catalog availability invented model mutation support")
	}
}
