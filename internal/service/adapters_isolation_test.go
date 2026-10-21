package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestAdapterCatalogHasCanonicalIdentityWithoutPerRowAccess(t *testing.T) {
	native := newAdapterFixture()
	native.sessions = make([]protocol.Session, 30)
	for i := range native.sessions {
		native.sessions[i] = protocol.Session{ID: payloadSession}
	}
	_, base, client := encryptedAdapterServer(t, []Adapter{fixtureAdapter("codex-desktop", native, nil), fixtureAdapter("codex-cli", native, nil)}, native)
	var canonical string
	for _, id := range []string{"codex-desktop", "codex-cli"} {
		status, data := adapterRequest(t, client, "GET", base+"/v1/adapters/"+id+"/sessions", nil, nil)
		var page protocol.SessionPage
		if status != 200 || json.Unmarshal(data, &page) != nil || len(page.Sessions) != 30 {
			t.Fatalf("catalog %d %s", status, data)
		}
		for _, row := range page.Sessions {
			if row.Access == nil || row.Access.Capabilities.IdleSend || len(row.Access.Capabilities.Settings) > 0 {
				t.Fatal("catalog grants control")
			}
			if canonical == "" {
				canonical = row.Access.CanonicalSessionRef
			}
			if row.Access.CanonicalSessionRef != canonical {
				t.Fatal("verified native identity split by access path")
			}
		}
	}
	if native.accessCalls != 0 {
		t.Fatalf("catalog caused %d native access reads", native.accessCalls)
	}
}

func TestAdapterPayloadRevisionsCannotCrossSourceOrNativeScope(t *testing.T) {
	first, second := newAdapterFixture(), newAdapterFixture()
	root, base, client := encryptedAdapterServer(t, []Adapter{fixtureAdapter("codex-desktop", first, nil), fixtureAdapter("codex-cli", second, nil)}, first)
	page := func(id string) protocol.TopologyPage {
		t.Helper()
		status, data := adapterRequest(t, client, "GET", base+"/v1/adapters/"+id+"/sessions/"+payloadSession+"/topology", nil, nil)
		var result protocol.TopologyPage
		if status != 200 || json.Unmarshal(data, &result) != nil || len(result.Nodes) == 0 || result.Access == nil {
			t.Fatalf("topology %d %s", status, data)
		}
		return result
	}
	desktop, cli := page("codex-desktop"), page("codex-cli")
	if desktop.Nodes[0].Revision == cli.Nodes[0].Revision {
		t.Fatal("revisions are not source scoped")
	}
	requested, _ := json.Marshal([]PayloadRequest{{ID: desktop.Nodes[0].ID, Revision: desktop.Nodes[0].Revision}})
	path := base + "/v1/adapters/codex-cli/sessions/" + payloadSession + "/payloads?messages=" + url.QueryEscape(string(requested))
	status, _ := adapterRequest(t, client, "GET", path, nil, nil)
	if status < 400 {
		t.Fatal("payload revision crossed source")
	}
	second.rotate("scope_b", "binding_b")
	status, _ = adapterRequest(t, client, "GET", path, nil, nil)
	if status < 400 {
		t.Fatal("payload revision crossed native account")
	}
	// Request views all retain the same device-level payload cache and reader cap.
	view, err := root.adapters["codex-cli"].view(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.server.payloads != root.payloads || view.server.events != root.events {
		t.Fatal("device budgets multiplied")
	}
	for i := 0; i < maxImageReaders; i++ {
		if !root.acquireImageReader() {
			t.Fatal("reader budget")
		}
	}
	if view.server.acquireImageReader() {
		t.Fatal("per-source reader budget bypass")
	}
	for i := 0; i < maxImageReaders; i++ {
		root.releaseImageReader()
	}
}

func privateAdapterServer(t *testing.T, native *adapterFixture, worker bool) (string, *http.Client) {
	t.Helper()
	options := ServerOptions{Token: testToken, Sessions: native, Adapters: []Adapter{fixtureAdapter("codex-desktop", native, nil)}}
	if worker {
		options.Identity = &ServiceIdentity{SessionMode: "standalone"}
	}
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	return httpServer.URL, &http.Client{Timeout: 3 * time.Second}
}

func TestAdapterPrivateWorkerRebuildsScopeFenceAndStripsMetadata(t *testing.T) {
	native := newAdapterFixture()
	base, client := privateAdapterServer(t, native, true)
	headers := map[string]string{"Authorization": "Bearer " + testToken}
	path := base + "/v1/sessions/" + payloadSession
	body := map[string]any{"model": "fixture-model", "accessBinding": "binding_a", "nativeScopeId": "scope_a"}
	status, data := adapterRequest(t, client, "POST", path+"/settings", body, headers)
	if status != 200 || native.dispatches != 1 || len(native.settings) != 1 || native.settings[0].Model.Value != "fixture-model" {
		t.Fatalf("private settings %d %s dispatch=%d", status, data, native.dispatches)
	}
	// Keep the binding unchanged so only the reconstructed scope callback can
	// reject an account change after the HTTP admission has already succeeded.
	native.beforeDispatch = func() { native.rotate("scope_b", "binding_a") }
	status, data = adapterRequest(t, client, "POST", path+"/messages", map[string]any{
		"text": "stale account", "clientId": payloadSession, "accessBinding": "binding_a", "nativeScopeId": "scope_a",
	}, headers)
	if status < 400 || native.dispatches != 1 {
		t.Fatalf("private final scope fence %d %s dispatch=%d", status, data, native.dispatches)
	}
}

func TestAdapterPrivateWorkerRejectsMissingOrStaleScopeMetadata(t *testing.T) {
	for _, test := range []struct {
		name    string
		worker  bool
		binding string
		scope   string
	}{
		{name: "missing-scope", worker: true, binding: "binding_a"},
		{name: "missing-binding", worker: true, scope: "scope_a"},
		{name: "stale-scope", worker: true, binding: "binding_a", scope: "scope_old"},
		{name: "stale-binding", worker: true, binding: "binding_old", scope: "scope_a"},
		{name: "public-legacy-route", binding: "binding_a", scope: "scope_a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			native := newAdapterFixture()
			base, client := privateAdapterServer(t, native, test.worker)
			body := map[string]any{"text": "must not dispatch", "clientId": payloadSession}
			if test.binding != "" {
				body["accessBinding"] = test.binding
			}
			if test.scope != "" {
				body["nativeScopeId"] = test.scope
			}
			status, data := adapterRequest(t, client, "POST", base+"/v1/sessions/"+payloadSession+"/messages", body,
				map[string]string{"Authorization": "Bearer " + testToken})
			if status < 400 || native.dispatches != 0 {
				t.Fatalf("private stale admission %d %s dispatch=%d", status, data, native.dispatches)
			}
		})
	}
}

func TestAdapterDiscoveryAndLegacyRoutesRemainIndependent(t *testing.T) {
	native := newAdapterFixture()
	root, base, client := encryptedAdapterServer(t, []Adapter{fixtureAdapter("codex-cli", native, nil)}, nil)
	status, data := adapterRequest(t, client, http.MethodGet, base+"/v1/info", nil, nil)
	var info infoResponse
	if status != 200 || json.Unmarshal(data, &info) != nil || info.Extensions["multiAdapter"] != 1 || len(info.Adapters) != 1 || info.SessionMode != "multi-source" || info.Agent == "codex" {
		t.Fatalf("info %d %s", status, data)
	}
	status, _ = adapterRequest(t, client, "GET", base+"/v1/sessions", nil, nil)
	if status != 503 {
		t.Fatal("CLI-only service impersonated Desktop")
	}
	root.adapters["codex-cli"].Scope = func(context.Context) (string, error) { return "", CodexRpcError("missing identity", -32600) }
	status, data = adapterRequest(t, client, "GET", base+"/v1/info", nil, nil)
	if status != 200 || json.Unmarshal(data, &info) != nil || len(info.Adapters) != 1 || info.Adapters[0].State != "unavailable" || info.Adapters[0].NativeScopeID == "scope_a" {
		t.Fatalf("unavailable source retained stale identity %d %s", status, data)
	}
}
