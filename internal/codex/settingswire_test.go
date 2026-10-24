package codex

import (
	"testing"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

// Exercise both native JSONL decoding and the private executor WebSocket hop.
// The wire shape comes from the installed CLI's generated v2 schema, not its
// snake_case rollout representation.
func TestNativeSettingsWireNotificationConfirmsEmptyAcknowledgement(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{admittedID: executorTaskID})
	host := &settingsWireHost{Rpc: f.client.Rpc, notify: func() {
		f.notify("thread/settings/updated", map[string]any{
			"threadId": executorTaskID,
			"threadSettings": map[string]any{
				"model": "native-model", "effort": "xhigh", "serviceTier": "default",
			},
		})
	}}
	session := NewNativeTaskSession(host)
	f.client.Rpc.SetHandlers(nil, func(packet map[string]any) {
		session.Receive(nativejson.Text(packet["method"]), packet["params"])
	})
	if err := session.Attach(executorTaskID); err != nil {
		t.Fatal(err)
	}
	if err := session.applySettings(executorTaskID, map[string]any{
		"threadId": executorTaskID, "model": "native-model", "effort": "xhigh",
	}, protocol.SessionSettings{Model: protocol.Known("native-model"), Effort: protocol.Known("xhigh")}); err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	settings := session.settings[executorTaskID]
	session.mu.Unlock()
	if settings.Model == nil || *settings.Model != "native-model" || settings.Effort == nil || *settings.Effort != "xhigh" ||
		!settings.ServiceTier.Known || settings.ServiceTier.Value != nil {
		t.Fatalf("wire settings = %+v", settings)
	}
	if f.countMethod("thread/settings/update") != 1 || f.countMethod("thread/resume") != 1 ||
		f.countMethod("turn/start") != 0 || f.countMethod("thread/start") != 0 {
		t.Fatal("settings replayed or started native work", f.callMethods())
	}
}

type settingsWireHost struct {
	*Rpc
	notify func()
}

func (h *settingsWireHost) Request(method string, params any) (any, error) {
	result, err := h.Rpc.Request(method, params)
	if method == "thread/settings/update" && err == nil {
		h.notify()
	}
	return result, err
}
