package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestCLIModelMetadataSurvivesSessionAccessAndEncryptedProjection(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "owned"
		if shared {
			name = "shared-daemon"
		}
		t.Run(name, func(t *testing.T) {
			surface, fixture := newCLIControlledFixture(t)
			metadata, _ := newCLIMetadataFixture(t)
			surface.metadata = metadata
			if _, _, err := fixture.sessions.Create(context.Background(), "first", "client", protocol.SessionSettings{}); err != nil {
				t.Fatal(err)
			}
			if shared {
				surface.shared = fixture.sessions
				surface.sharedFactory = &cliSharedFactory{tasks: map[string]cliSharedRecord{taskSessionsTaskID: {}}}
			}
			access, err := surface.Access(context.Background(), taskSessionsTaskID)
			if err != nil || !access.Capabilities.Models || !access.Capabilities.Usage {
				t.Fatalf("session lost source metadata: access=%+v err=%v", access, err)
			}
			adapter := Adapter{ID: codexCLISource, Family: "codex", Origin: "cli", Scope: surface.scope,
				Sessions: surface, Capabilities: surface.Capabilities(), Usage: surface.Usage,
				Status: func() AdapterStatus { return AdapterStatus{State: "connected"} }}
			_, base, client := encryptedAdapterServer(t, []Adapter{adapter}, nil)
			status, data := adapterRequest(t, client, "GET", base+"/v1/adapters/codex-cli/sessions/"+taskSessionsTaskID, nil, nil)
			var page protocol.ConversationPage
			if status != 200 || json.Unmarshal(data, &page) != nil || page.Access == nil ||
				!page.Access.Capabilities.Models || !page.Access.Capabilities.Usage {
				t.Fatalf("encrypted page lost metadata: status=%d page=%+v", status, page.Access)
			}
			status, data = adapterRequest(t, client, "GET", base+"/v1/adapters/codex-cli/models", nil, nil)
			var catalog struct {
				Models []protocol.Model `json:"models"`
			}
			if status != 200 || json.Unmarshal(data, &catalog) != nil || len(catalog.Models) != 1 || catalog.Models[0].Name != "Native model" {
				t.Fatalf("native catalog unavailable: status=%d data=%s", status, data)
			}
			status, data = adapterRequest(t, client, "POST", base+"/v1/adapters/codex-cli/sessions/"+taskSessionsTaskID+"/settings",
				map[string]any{"effort": "low", "accessBinding": page.Access.Binding}, nil)
			if status != 200 {
				t.Fatalf("effort update rejected: status=%d data=%s", status, data)
			}
			status, data = adapterRequest(t, client, "GET", base+"/v1/adapters/codex-cli/sessions/"+taskSessionsTaskID, nil, nil)
			if status != 200 || json.Unmarshal(data, &page) != nil || page.Runtime == nil || !page.Runtime.Effort.Known || page.Runtime.Effort.Value == nil || *page.Runtime.Effort.Value != "low" {
				t.Fatalf("fresh native effort missing: status=%d runtime=%+v", status, page.Runtime)
			}
			if fixture.peer.callCount("thread/settings/update") != 1 || fixture.peer.callCount("thread/resume") != 0 || fixture.peer.callCount("turn/start") != 1 {
				t.Fatal("settings replayed or acquired another writer", fixture.peer.calls())
			}
		})
	}
}
