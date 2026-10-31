package claude

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestModConfigAliasesNeverBecomeSelectableModels(t *testing.T) {
	for _, origin := range []string{"terminal", "desktop"} {
		t.Run(origin, func(t *testing.T) {
			a := fixtureAdapter(t)
			a.origin = origin
			code, registered := bridgeCall(t, a, "register", registerRequest{SessionID: fixtureSession,
				Instance: "model-catalog-fixture", Origin: origin})
			if code != http.StatusOK {
				t.Fatal("source registration refused", code)
			}
			nonce := registered.Nonce
			yes := true
			model, effort := "deepseek-v4-flash[1m]", "high"
			code, _ = bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
				Events: []modEvent{{Kind: "snapshot", ModelOptions: []string{"default", "sonnet", "opus[1m]"},
					Model: &model, Effort: &effort, CanSettings: &yes}}})
			if code != http.StatusOK {
				t.Fatal("config snapshot rejected", code)
			}
			models, err := a.Models(context.Background())
			if err == nil || len(models) != 0 {
				t.Fatalf("config aliases became a model catalog: %+v %v", models, err)
			}
			access, err := a.Access(context.Background(), fixtureSession)
			if err != nil || access.Capabilities.Models || !reflect.DeepEqual(access.Capabilities.Settings, []string{"effort"}) {
				t.Fatalf("alias removal lost request effort or retained model control: %+v %v", access, err)
			}
			page, err := a.Read(context.Background(), fixtureSession, "", false, nil)
			if err != nil || page.Runtime.Model == nil || *page.Runtime.Model != model || !page.Runtime.Effort.Known || page.Runtime.Effort.Value == nil || *page.Runtime.Effort.Value != effort {
				t.Fatal("catalog refusal lost actual native runtime readings", err)
			}
			ctx := protocol.WithAccessBinding(context.Background(), access.Binding)
			for _, choice := range []string{"sonnet", model} {
				if err := a.SetModel(ctx, fixtureSession, choice); err == nil {
					t.Fatal("unresolved model choice admitted", choice)
				}
			}
			_, pulled := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
			if len(pulled.Commands) != 0 || a.registrations[fixtureSession].settingsChange != nil {
				t.Fatal("refused model selection queued a native mutation")
			}
		})
	}
}

func TestModConfigAliasesRejectInvalidBatchAtomically(t *testing.T) {
	for _, options := range [][]string{{"same", "same"}, {""}, {" spaced"}, {"bad\nmodel"}, {strings.Repeat("m", 257)}, make([]string, 33)} {
		a := fixtureAdapter(t)
		nonce := registerFixture(t, a, fixtureSession)
		bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
			Events: []modEvent{{Kind: "snapshot", ModelOptions: []string{"original"}}}})
		code, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 2,
			Events: []modEvent{{Kind: "start", TurnID: "must-not-start"}, {Kind: "snapshot", ModelOptions: options}}})
		if code != http.StatusConflict || !reflect.DeepEqual(a.registrations[fixtureSession].modelOptions, []string{"original"}) || readRuntimeFixture(t, a).ActiveTurnID != nil {
			t.Fatal("invalid config aliases partially changed native state")
		}
	}
	if err := validateEvent(modEvent{Kind: "busy", ModelOptions: []string{"bad-placement"}}); err == nil {
		t.Fatal("config aliases accepted outside native snapshot")
	}
}
