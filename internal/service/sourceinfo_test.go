package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestGatewayInfoReadsOneFreshWorkerSnapshot(t *testing.T) {
	var calls atomic.Int32
	var revision atomic.Int32
	revision.Store(1)
	ids := []string{codexCLISource, claudeCodeSource, claudeDesktopSource}
	privateAdapters := make([]Adapter, 0, len(ids))
	for _, id := range ids {
		adapter := fixtureAdapter(id, newAdapterFixture(), nil)
		adapter.Scope = func(context.Context) (string, error) {
			if revision.Load() == 1 {
				return "scope_first", nil
			}
			return "scope_second", nil
		}
		adapter.CapabilitySource = func(context.Context) (protocol.AdapterCapabilities, error) {
			caps := fixtureCaps()
			caps.History = revision.Load() == 1
			return caps, nil
		}
		privateAdapters = append(privateAdapters, adapter)
	}
	private := newTestServer(t, ServerOptions{Adapters: privateAdapters})
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/info" {
			calls.Add(1)
			timer := time.NewTimer(275 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-request.Context().Done():
				return
			}
			if revision.Load() == 3 {
				http.Error(w, `{"error":"offline"}`, http.StatusServiceUnavailable)
				return
			}
		}
		private.Config.Handler.ServeHTTP(w, request)
	}))
	defer endpoint.Close()
	worker, err := NewUserWorker(endpoint.URL, StaticUserWorkerToken(testToken), nil, "", DefaultUserWorkerTimeouts)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	adapters := gatewaySourceAdapters(ids, worker, func(string, string) {}, Adapter{}, nil)
	_, base, client := encryptedAdapterServer(t, adapters, nil)
	for iteration := int32(1); iteration <= 3; iteration++ {
		revision.Store(iteration)
		started := time.Now()
		status, body := adapterRequest(t, client, http.MethodGet, base+"/v1/info", nil, nil)
		elapsed := time.Since(started)
		var info infoResponse
		if status != http.StatusOK || json.Unmarshal(body, &info) != nil || len(info.Adapters) != len(ids) {
			t.Fatalf("invalid source discovery: status=%d", status)
		}
		if calls.Load() != iteration {
			t.Fatalf("public discovery repeated complete worker discovery: calls=%d requests=%d elapsed=%s", calls.Load(), iteration, elapsed)
		}
		if elapsed >= 2*time.Second {
			t.Fatalf("discovery exceeds installed readiness budget: %s", elapsed)
		}
		for _, descriptor := range info.Adapters {
			expectedScope := "scope_first"
			if iteration == 2 {
				expectedScope = "scope_second"
			}
			if iteration == 3 {
				if descriptor.State != "unavailable" || descriptor.Capabilities.History || descriptor.Capabilities.Stop {
					t.Fatal("failed discovery retained earlier authority")
				}
				continue
			}
			if descriptor.NativeScopeID != expectedScope || descriptor.Capabilities.History != (iteration == 1) {
				t.Fatalf("discovery reused stale evidence: source=%s scope=%s history=%v", descriptor.ID, descriptor.NativeScopeID, descriptor.Capabilities.History)
			}
		}
	}
}
