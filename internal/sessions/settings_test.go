package sessions

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestDesktopSettingsRequireFreshOwnerReadback(t *testing.T) {
	for _, mode := range []string{"stale", "fresh", "disconnected", "usagePatch", "unrelatedSettingsPatch", "freshEffortPatch", "unusedEffortFallback"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				state := desktopState("thread", "completed", "idle")
				state["latestModel"] = "model"
				state["latestReasoningEffort"] = "low"
				state["latestThreadSettings"] = map[string]any{"effort": "low", "serviceTier": nil}
				base := &baseFixture{models: []protocol.Model{{ID: "model", ReasoningEfforts: []string{"low"}}}}
				f := newControlSessions(state, base, nil)
				defer f.sessions.Close()
				switch mode {
				case "fresh":
					// Native application can be published before its IPC ACK arrives.
					f.ipc.settingsApplied = func() { f.sessions.f.Receive(snapshotOf("thread", "original", 2, state)) }
				case "disconnected":
					f.ipc.settingsApplied = func() { f.sessions.f.Disconnected() }
				case "usagePatch":
					f.ipc.settingsApplied = func() {
						sendSettingsPatches(f.sessions, 2, map[string]any{"op": "add", "path": []any{"latestTokenUsageInfo"}, "value": map[string]any{}})
					}
				case "unrelatedSettingsPatch":
					f.ipc.settingsApplied = func() {
						sendSettingsPatches(f.sessions, 2, map[string]any{"op": "replace", "path": []any{"latestThreadSettings", "serviceTier"}, "value": nil})
					}
				case "freshEffortPatch":
					f.ipc.settingsApplied = func() {
						sendSettingsPatches(f.sessions, 2, map[string]any{"op": "replace", "path": []any{"latestThreadSettings", "effort"}, "value": "low"})
					}
				case "unusedEffortFallback":
					f.ipc.settingsApplied = func() {
						sendSettingsPatches(f.sessions, 2, map[string]any{"op": "replace", "path": []any{"latestReasoningEffort"}, "value": "low"})
					}
				}
				err := f.sessions.UpdateSettings(context.Background(), "thread", protocol.SessionSettings{Effort: protocol.Known("low")})
				wantSuccess := mode == "fresh" || mode == "freshEffortPatch"
				if (err == nil) != wantSuccess {
					t.Fatalf("%s native readback: %v", mode, err)
				}
				if len(f.ipc.operations()) != 1 {
					t.Fatal("Unconfirmed settings were replayed", f.ipc.operations())
				}
			})
		})
	}
}

func TestDesktopSettingsRequireEveryRequestedField(t *testing.T) {
	for _, mode := range []string{"effortOnly", "tierOnly", "bothFields", "settingsObject", "rootState", "fallbackEffort"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				state := desktopState("thread", "completed", "idle")
				state["latestModel"] = "model"
				state["latestThreadSettings"] = map[string]any{"effort": "low", "serviceTier": nil}
				if mode == "fallbackEffort" {
					state["latestThreadSettings"] = map[string]any{"serviceTier": nil}
					state["latestReasoningEffort"] = "low"
				}
				base := &baseFixture{models: []protocol.Model{{ID: "model", ReasoningEfforts: []string{"low"}}}}
				f := newControlSessions(state, base, nil)
				defer f.sessions.Close()
				f.ipc.settingsApplied = func() {
					effort := map[string]any{"op": "replace", "path": []any{"latestThreadSettings", "effort"}, "value": "low"}
					tier := map[string]any{"op": "replace", "path": []any{"latestThreadSettings", "serviceTier"}, "value": nil}
					switch mode {
					case "effortOnly":
						sendSettingsPatches(f.sessions, 2, effort)
					case "tierOnly":
						sendSettingsPatches(f.sessions, 2, tier)
					case "bothFields":
						sendSettingsPatches(f.sessions, 2, effort)
						sendSettingsPatches(f.sessions, 3, tier)
					case "settingsObject":
						sendSettingsPatches(f.sessions, 2, map[string]any{"op": "replace", "path": []any{"latestThreadSettings"}, "value": state["latestThreadSettings"]})
					case "rootState":
						sendSettingsPatches(f.sessions, 2, map[string]any{"op": "replace", "path": []any{}, "value": state})
					case "fallbackEffort":
						sendSettingsPatches(f.sessions, 2, map[string]any{"op": "replace", "path": []any{"latestReasoningEffort"}, "value": "low"}, tier)
					}
				}
				err := f.sessions.UpdateSettings(context.Background(), "thread", protocol.SessionSettings{
					Effort: protocol.Known("low"), ServiceTier: protocol.Known[*string](nil),
				})
				wantSuccess := mode != "effortOnly" && mode != "tierOnly"
				if (err == nil) != wantSuccess {
					t.Fatalf("%s native readback: %v", mode, err)
				}
			})
		})
	}
}

func sendSettingsPatches(sessions *DesktopSessions, revision int64, patches ...any) {
	message := snapshotOf("thread", "original", float64(revision), nil)
	fields(message["params"])["change"] = map[string]any{
		"type": "patches", "revision": float64(revision), "baseRevision": float64(revision - 1), "patches": patches,
	}
	sessions.Receive(message)
}
