package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/newo-ether/filo/internal/claude"
	"github.com/newo-ether/filo/internal/protocol"
)

func TestClaudeExecutorCeilingIncludesOnlyConfiguredModSteering(t *testing.T) {
	for _, withMod := range []bool{false, true} {
		executor := &ClaudeExecutorSessions{}
		if withMod {
			executor.options.ReadOnly = &claude.Adapter{}
		}
		caps := executor.Capabilities()
		if (caps.ActiveSend == "native-steer") != withMod || !caps.Create || !caps.Rename {
			t.Fatalf("withMod=%v capabilities=%+v", withMod, caps)
		}
		owned := &claude.NativeSession{}
		if access := intersectCaps(owned.Access().Capabilities, caps); access.ActiveSend != "unsupported" {
			t.Fatal("source union granted steering to an owned stream-json session")
		}
	}
}

type claudeAssemblyAccessFixture struct {
	*adapterFixture
	restricted atomic.Bool
}

func (f *claudeAssemblyAccessFixture) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	access, err := f.adapterFixture.Access(ctx, id)
	if f.restricted.Load() {
		access.Capabilities = readOnlyCaps(access.Capabilities)
	}
	return access, err
}

func TestAssemblyClaudeControlsSurvivePublicCeilingAndKeepFreshRestrictions(t *testing.T) {
	for _, id := range []string{claudeCodeSource, claudeDesktopSource} {
		t.Run(id, func(t *testing.T) {
			native := &claudeAssemblyAccessFixture{adapterFixture: newAdapterFixture()}
			var workerRestricted atomic.Bool
			privateAdapter := fixtureAdapter(id, native.adapterFixture, nil)
			privateAdapter.Family, privateAdapter.Sessions = "claude", native
			privateAdapter.Capabilities = claude.Capabilities()
			privateAdapter.CapabilitySource = func(context.Context) (protocol.AdapterCapabilities, error) {
				caps := claude.Capabilities()
				if workerRestricted.Load() {
					caps = readOnlyCaps(caps)
				}
				return caps, nil
			}
			private, err := NewServer(ServerOptions{Token: testToken, Adapters: []Adapter{privateAdapter}})
			if err != nil {
				t.Fatal(err)
			}
			endpoint := httptest.NewServer(private)
			defer endpoint.Close()
			worker, err := NewUserWorker(endpoint.URL, StaticUserWorkerToken(testToken), nil, "", DefaultUserWorkerTimeouts)
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			adapters := gatewaySourceAdapters([]string{id}, worker, func(string, string) {}, Adapter{}, nil)
			_, base, client := encryptedAdapterServer(t, adapters, nil)
			for phase := 0; phase < 3; phase++ {
				native.restricted.Store(phase == 1)
				workerRestricted.Store(phase == 2)
				status, data := adapterRequest(t, client, http.MethodGet, base+"/v1/info", nil, nil)
				var info infoResponse
				if status != http.StatusOK || json.Unmarshal(data, &info) != nil || len(info.Adapters) != 1 {
					t.Fatalf("phase=%d discovery status=%d", phase, status)
				}
				caps := info.Adapters[0].Capabilities
				if (caps.ActiveSend == "native-steer") != (phase != 2) || caps.IdleSend != (phase != 2) || caps.Stop != (phase != 2) || !caps.Models || (len(caps.Settings) == 2) != (phase != 2) {
					t.Fatalf("phase=%d source controls=%+v", phase, caps)
				}
				if caps.Create || caps.Rename || caps.Archive || caps.Uploads || caps.Usage {
					t.Fatalf("source gained capabilities absent from its private owner: %+v", caps)
				}
				status, data = adapterRequest(t, client, http.MethodGet, base+"/v1/adapters/"+id+"/sessions/"+payloadSession, nil, nil)
				var page protocol.ConversationPage
				if status != http.StatusOK || json.Unmarshal(data, &page) != nil || page.Access == nil {
					t.Fatalf("phase=%d session status=%d", phase, status)
				}
				caps = page.Access.Capabilities
				if (caps.ActiveSend == "native-steer") != (phase == 0) || caps.IdleSend != (phase == 0) || caps.Stop != (phase == 0) || (len(caps.Settings) == 2) != (phase == 0) {
					t.Fatalf("phase=%d session controls=%+v", phase, caps)
				}
			}
		})
	}
}
