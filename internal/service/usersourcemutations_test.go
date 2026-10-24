package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

type sourceMutationNative struct{ *adapterFixture }

func (f *sourceMutationNative) SetModel(ctx context.Context, id, model string) error {
	if err := f.dispatch(ctx, id); err != nil {
		return err
	}
	return f.fakeSessions.SetModel(ctx, id, model)
}

func (f *sourceMutationNative) Stop(ctx context.Context, id, turn string) error {
	if err := f.dispatch(ctx, id); err != nil {
		return err
	}
	return f.fakeSessions.Stop(ctx, id, turn)
}

func (f *sourceMutationNative) Rename(ctx context.Context, id, name string) (protocol.Session, error) {
	if err := f.dispatch(ctx, id); err != nil {
		return protocol.Session{}, err
	}
	return f.fakeSessions.Rename(ctx, id, name)
}

func (f *sourceMutationNative) Archive(ctx context.Context, id string) (string, error) {
	if err := f.dispatch(ctx, id); err != nil {
		return "", err
	}
	return f.fakeSessions.Archive(ctx, id)
}

type sourceMutationRequest struct {
	path, scope, bindingHeader string
	body                       map[string]any
}

type sourceMutationChain struct {
	native      *sourceMutationNative
	other       *adapterFixture
	privateRoot *Server
	privateURL  string
	publicURL   string
	client      *http.Client
	source      *userSource
	mu          sync.Mutex
	requests    []sourceMutationRequest
	rewrite     func(*http.Request, *httptest.ResponseRecorder)
}

func newSourceMutationChain(t *testing.T, id string) *sourceMutationChain {
	t.Helper()
	chain := &sourceMutationChain{native: &sourceMutationNative{newAdapterFixture()}, other: newAdapterFixture()}
	chain.native.cwd = "fixture-workspace"
	adapter := fixtureAdapter(id, chain.native.adapterFixture, nil)
	adapter.Sessions = chain.native
	if strings.HasPrefix(id, "claude") {
		adapter.Family = "claude"
	}
	private, err := NewServer(ServerOptions{Token: strings.Repeat("b", 64), Identity: &ServiceIdentity{SessionMode: "standalone"},
		Adapters: []Adapter{adapter, fixtureAdapter(codexDesktopSource, chain.other, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	chain.privateRoot = private
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed := sourceMutationRequest{path: r.URL.Path, scope: r.URL.Query().Get("nativeScopeId"),
			bindingHeader: r.Header.Get("X-Filo-Access-Binding")}
		if r.Method == http.MethodPost {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			_ = json.Unmarshal(raw, &observed.body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		chain.mu.Lock()
		chain.requests = append(chain.requests, observed)
		rewrite := chain.rewrite
		chain.mu.Unlock()
		recorder := httptest.NewRecorder()
		private.ServeHTTP(recorder, r)
		if rewrite != nil {
			rewrite(r, recorder)
		}
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	t.Cleanup(server.Close)
	chain.privateURL = server.URL
	worker := newUserWorkerFixture(t, server.URL, StaticUserWorkerToken(strings.Repeat("b", 64)), userWorkerUnusedHistory{}, DefaultUserWorkerTimeouts)
	chain.source = newUserSource(worker, id, nil)
	// Fixture ceilings let every forwarding action run through both production
	// servers. Real source assembly independently narrows these permissions.
	publicAdapter := Adapter{ID: id, Family: adapter.Family, Origin: "cli", Scope: chain.source.scope,
		Sessions: chain.source, Capabilities: fixtureCaps(), CapabilitySource: chain.source.capabilities}
	_, chain.publicURL, chain.client = encryptedAdapterServer(t, []Adapter{publicAdapter}, nil)
	return chain
}

func (chain *sourceMutationChain) setRewrite(rewrite func(*http.Request, *httptest.ResponseRecorder)) {
	chain.mu.Lock()
	chain.rewrite = rewrite
	chain.mu.Unlock()
}

func (chain *sourceMutationChain) writes() []sourceMutationRequest {
	chain.mu.Lock()
	defer chain.mu.Unlock()
	var writes []sourceMutationRequest
	for _, request := range chain.requests {
		if request.body != nil {
			writes = append(writes, request)
		}
	}
	return writes
}

func TestUserSourceMutationsTraverseAuthenticatedPrivateAndEncryptedPublic(t *testing.T) {
	for _, id := range []string{codexCLISource, claudeCodeSource} {
		t.Run(id, func(t *testing.T) {
			chain := newSourceMutationChain(t, id)
			binding := fixtureBinding(t, chain.client, chain.publicURL, id)
			view, err := chain.privateRoot.adapters[id].view(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			privateBinding := view.seal("binding", payloadSession, "binding_a")
			if binding == privateBinding {
				t.Fatal("public binding did not wrap the private binding")
			}
			actions := []struct {
				action string
				body   map[string]any
				status int
			}{
				{"messages", map[string]any{"text": "exact input", "clientId": payloadSession}, 202},
				{"settings", map[string]any{"model": "fixture-model", "serviceTier": nil}, 200},
				{"model", map[string]any{"model": "fixture-model"}, 200},
				{"stop", map[string]any{"turnId": payloadSession}, 200},
				{"rename", map[string]any{"name": "fixture renamed"}, 200},
				{"archive", map[string]any{}, 200},
			}
			for _, action := range actions {
				action.body["accessBinding"] = binding
				status, data := adapterRequest(t, chain.client, "POST", chain.publicURL+"/v1/adapters/"+id+"/sessions/"+payloadSession+"/"+action.action, action.body, nil)
				if status != action.status {
					t.Errorf("%s: %d %s", action.action, status, data)
				}
			}
			writes := chain.writes()
			if len(writes) != len(actions) || chain.native.dispatches != len(actions) || chain.other.dispatches != 0 {
				t.Fatalf("writes=%d dispatches=%d alternate=%d", len(writes), chain.native.dispatches, chain.other.dispatches)
			}
			for i, write := range writes {
				if write.path != "/v1/adapters/"+id+"/sessions/"+payloadSession+"/"+actions[i].action ||
					write.body["accessBinding"] != privateBinding || write.bindingHeader != "" {
					t.Fatalf("wrong private forwarding for %s: %+v", actions[i].action, write)
				}
				if _, exists := write.body["nativeScopeId"]; exists {
					t.Fatalf("creation-only scope field leaked into %s", actions[i].action)
				}
			}
			if len(chain.native.settings) != 1 || !chain.native.settings[0].ServiceTier.Known ||
				chain.native.settings[0].ServiceTier.Value != nil || chain.native.settings[0].Effort.Known {
				t.Fatal("settings omission or explicit native default was lost")
			}
		})
	}
}

func TestUserSourceCreateForwardsCapturedScopeAndSettings(t *testing.T) {
	chain := newSourceMutationChain(t, codexCLISource)
	path := chain.publicURL + "/v1/adapters/codex-cli/sessions"
	body := map[string]any{"text": "first native input", "clientId": payloadSession, "nativeScopeId": "scope_old"}
	status, _ := adapterRequest(t, chain.client, "POST", path, body, nil)
	if status < 400 || len(chain.writes()) != 0 {
		t.Fatal("stale creation draft reached private worker")
	}
	body["nativeScopeId"] = "scope_a"
	body["settings"] = map[string]any{"model": "fixture-model", "serviceTier": nil}
	status, data := adapterRequest(t, chain.client, "POST", path, body, nil)
	var receipt createSessionResponse
	if status != 201 || json.Unmarshal(data, &receipt) != nil || receipt.ID != payloadSession ||
		receipt.TurnID != "turn-1" || receipt.ClientID != payloadSession || receipt.Access == nil {
		t.Fatalf("create %d %s", status, data)
	}
	writes := chain.writes()
	if len(writes) != 1 || chain.native.dispatches != 1 || chain.other.dispatches != 0 ||
		writes[0].path != "/v1/adapters/codex-cli/sessions" || writes[0].body["text"] != body["text"] ||
		writes[0].body["clientId"] != payloadSession || writes[0].body["nativeScopeId"] != "scope_a" {
		t.Fatalf("creation forwarding: %+v", writes)
	}
	if _, exists := writes[0].body["accessBinding"]; exists {
		t.Fatal("creation inherited an existing session binding")
	}
	settings, ok := writes[0].body["settings"].(map[string]any)
	if !ok || len(settings) != 2 || settings["model"] != "fixture-model" {
		t.Fatal("creation settings were not preserved")
	}
	if value, exists := settings["serviceTier"]; !exists || value != nil {
		t.Fatal("creation lost explicit native default")
	}
}

func TestUserSourceRejectsStaleBindingAndScopeSwapWithoutReplay(t *testing.T) {
	for _, change := range []string{"binding", "scope", "final-binding", "final-scope", "create-scope"} {
		t.Run(change, func(t *testing.T) {
			chain := newSourceMutationChain(t, codexCLISource)
			binding := fixtureBinding(t, chain.client, chain.publicURL, codexCLISource)
			body := map[string]any{"text": "one input", "clientId": payloadSession, "accessBinding": binding}
			path := chain.publicURL + "/v1/adapters/codex-cli/sessions/" + payloadSession + "/messages"
			switch change {
			case "binding":
				chain.native.rotate("scope_a", "binding_b")
			case "scope":
				chain.native.rotate("scope_b", "binding_b")
			case "final-binding":
				chain.native.beforeDispatch = func() { chain.native.rotate("scope_a", "binding_b") }
			case "final-scope", "create-scope":
				chain.native.beforeDispatch = func() { chain.native.rotate("scope_b", "binding_b") }
			}
			if change == "create-scope" {
				path = chain.publicURL + "/v1/adapters/codex-cli/sessions"
				delete(body, "accessBinding")
				body["nativeScopeId"] = "scope_a"
			}
			status, _ := adapterRequest(t, chain.client, "POST", path, body, nil)
			expectedWrites := 0
			if strings.HasPrefix(change, "final-") || change == "create-scope" {
				expectedWrites = 1
			}
			if status < 400 || chain.native.dispatches != 0 || chain.other.dispatches != 0 || len(chain.writes()) != expectedWrites {
				t.Fatalf("unsafe dispatch: status=%d native=%d other=%d writes=%d", status, chain.native.dispatches, chain.other.dispatches, len(chain.writes()))
			}
		})
	}
}

func TestUserSourceAccessIsFreshAndScopeFenced(t *testing.T) {
	chain := newSourceMutationChain(t, codexCLISource)
	first, err := chain.source.Access(context.Background(), payloadSession)
	if err != nil {
		t.Fatal(err)
	}
	chain.source.remember(payloadSession, &first)
	chain.native.rotate("scope_a", "binding_b")
	second, err := chain.source.Access(context.Background(), payloadSession)
	if err != nil || first.Binding == second.Binding || !second.Capabilities.IdleSend {
		t.Fatalf("access reused catalog authority: %v", err)
	}
	ctx := protocol.WithNativeScopeID(context.Background(), "scope_a")
	if chain.source.ValidateAccess(ctx, payloadSession, first.Binding) == nil {
		t.Fatal("stale cached binding passed validation")
	}
	chain.setRewrite(func(r *http.Request, response *httptest.ResponseRecorder) {
		if strings.HasSuffix(r.URL.Path, "/access") && response.Code == 200 {
			chain.native.rotate("scope_b", "binding_b")
		}
	})
	if _, err := chain.source.Access(ctx, payloadSession); err == nil {
		t.Fatal("access escaped across a private account change")
	}
	chain.mu.Lock()
	defer chain.mu.Unlock()
	for _, request := range chain.requests {
		if strings.HasSuffix(request.path, "/access") && request.scope != "scope_a" {
			t.Fatal("access lookup omitted captured scope")
		}
	}
}

func TestUserSourceCatalogAccessDoesNotReadControlAuthority(t *testing.T) {
	chain := newSourceMutationChain(t, codexCLISource)
	if _, err := chain.source.scope(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := chain.source.List(context.Background(), "")
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].Access == nil {
		t.Fatalf("catalog: %v", err)
	}
	chain.mu.Lock()
	before := len(chain.requests)
	chain.mu.Unlock()
	for i := 0; i < 30; i++ {
		access, err := chain.source.CatalogAccess(context.Background(), payloadSession)
		if err != nil || access.Capabilities.IdleSend || access.Capabilities.Create || access.Capabilities.Stop {
			t.Fatalf("catalog control: %+v %v", access.Capabilities, err)
		}
	}
	chain.mu.Lock()
	after := len(chain.requests)
	chain.mu.Unlock()
	if before != after || chain.native.accessCalls != 0 || chain.native.dispatches != 0 {
		t.Fatalf("catalog caused native access: requests=%d->%d access=%d", before, after, chain.native.accessCalls)
	}
}

func TestUserSourceAccessRouteIsWorkerOnlyAndAuthenticated(t *testing.T) {
	chain := newSourceMutationChain(t, codexCLISource)
	path := "/v1/adapters/codex-cli/sessions/" + payloadSession + "/access?nativeScopeId=scope_a"
	for _, headers := range []map[string]string{nil, {"Authorization": "Bearer invalid"}} {
		status, _ := adapterRequest(t, http.DefaultClient, "GET", chain.privateURL+path, nil, headers)
		if status != 401 {
			t.Fatalf("unauthenticated private access: %d", status)
		}
	}
	privateHeaders := map[string]string{"Authorization": "Bearer " + strings.Repeat("b", 64)}
	status, _ := adapterRequest(t, http.DefaultClient, "GET", chain.privateURL+path, nil, privateHeaders)
	if status != 200 {
		t.Fatalf("worker access: %d", status)
	}
	status, _ = adapterRequest(t, chain.client, "GET", chain.publicURL+path, nil, nil)
	if status != 404 {
		t.Fatalf("public access endpoint exposed: %d", status)
	}
	native := newAdapterFixture()
	nonworker, err := NewServer(ServerOptions{Token: testToken, Adapters: []Adapter{fixtureAdapter(codexCLISource, native, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(nonworker)
	defer server.Close()
	status, _ = adapterRequest(t, http.DefaultClient, "GET", server.URL+path, nil, authHeaders())
	if status != 404 || native.accessCalls != 0 {
		t.Fatalf("non-worker access: %d reads=%d", status, native.accessCalls)
	}
}

func TestUserSourceMutationsRejectUnconfirmedReceiptsWithoutRetry(t *testing.T) {
	cases := []struct {
		name, action string
		body         map[string]any
		reply        any
	}{
		{"send-client", "messages", map[string]any{"text": "one input", "clientId": payloadSession}, protocol.SendReceipt{TurnID: "turn", ClientID: "other-client"}},
		{"send-turn", "messages", map[string]any{"text": "one input", "clientId": payloadSession}, protocol.SendReceipt{ClientID: payloadSession}},
		{"stop", "stop", map[string]any{"turnId": payloadSession}, map[string]any{"stopped": false}},
		{"model", "model", map[string]any{"model": "fixture-model"}, map[string]any{"updated": false}},
		{"rename", "rename", map[string]any{"name": "fixture renamed"}, protocol.Session{ID: "different-session"}},
		{"archive", "archive", map[string]any{}, map[string]any{"archived": true, "cwd": nil}},
		{"create-session", "", map[string]any{"text": "first input", "clientId": payloadSession}, createSessionResponse{Session: protocol.Session{ID: ".."}, TurnID: "turn", ClientID: payloadSession}},
		{"create-client", "", map[string]any{"text": "first input", "clientId": payloadSession}, createSessionResponse{Session: protocol.Session{ID: payloadSession}, TurnID: "turn", ClientID: "other-client"}},
		{"create-turn", "", map[string]any{"text": "first input", "clientId": payloadSession}, createSessionResponse{Session: protocol.Session{ID: payloadSession}, ClientID: payloadSession}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			chain := newSourceMutationChain(t, codexCLISource)
			binding := fixtureBinding(t, chain.client, chain.publicURL, codexCLISource)
			chain.setRewrite(func(r *http.Request, response *httptest.ResponseRecorder) {
				if r.Method == http.MethodPost && response.Code < 300 {
					response.Body.Reset()
					_ = json.NewEncoder(response.Body).Encode(test.reply)
				}
			})
			path := chain.publicURL + "/v1/adapters/codex-cli/sessions"
			if test.action == "" {
				test.body["nativeScopeId"] = "scope_a"
			} else {
				path += "/" + payloadSession + "/" + test.action
				test.body["accessBinding"] = binding
			}
			status, _ := adapterRequest(t, chain.client, "POST", path, test.body, nil)
			if status < 400 || len(chain.writes()) != 1 || chain.native.dispatches != 1 || chain.other.dispatches != 0 {
				t.Fatalf("unconfirmed receipt: status=%d writes=%d dispatch=%d alternate=%d", status, len(chain.writes()), chain.native.dispatches, chain.other.dispatches)
			}
		})
	}
}

func TestUserSourceControlRequiresScopeAndBindingBeforePrivateDispatch(t *testing.T) {
	chain := newSourceMutationChain(t, codexCLISource)
	access, err := chain.source.Access(context.Background(), payloadSession)
	if err != nil {
		t.Fatal(err)
	}
	contexts := []context.Context{
		context.Background(),
		protocol.WithNativeScopeID(context.Background(), "scope_a"),
		protocol.WithAccessBinding(context.Background(), access.Binding),
		protocol.WithAccessBinding(protocol.WithNativeScopeID(context.Background(), "scope_old"), access.Binding),
	}
	for _, ctx := range contexts {
		if _, err := chain.source.Send(ctx, payloadSession, "input", payloadSession); err == nil {
			t.Fatal("unscoped private control was accepted")
		}
	}
	if _, _, err := chain.source.Create(context.Background(), "input", payloadSession, protocol.SessionSettings{}); err == nil {
		t.Fatal("creation without captured scope was accepted")
	}
	if len(chain.writes()) != 0 || chain.native.dispatches != 0 {
		t.Fatal("invalid control reached private mutation")
	}
}

func TestUserSourceAssemblyNarrowingDisablesPrivateReadOnlyControl(t *testing.T) {
	chain := newSourceMutationChain(t, codexCLISource)
	chain.privateRoot.adapters[codexCLISource].Capabilities = readOnlyCaps(fixtureCaps())
	adapters := gatewaySourceAdapters([]string{codexCLISource}, chain.source.worker, func(string, string) {}, Adapter{}, nil)
	_, base, client := encryptedAdapterServer(t, adapters, nil)
	status, raw := adapterRequest(t, client, "GET", base+"/v1/info", nil, nil)
	var info struct {
		Adapters []protocol.AdapterDescriptor `json:"adapters"`
	}
	if status != 200 || json.Unmarshal(raw, &info) != nil || len(info.Adapters) != 1 {
		t.Fatalf("assembly discovery: %d %s", status, raw)
	}
	caps := info.Adapters[0].Capabilities
	if !caps.History || caps.Create || caps.IdleSend || caps.Stop || len(caps.Settings) != 0 {
		t.Fatalf("private read-only evidence did not narrow ceiling: %+v", caps)
	}
	status, _ = adapterRequest(t, client, "POST", base+"/v1/adapters/codex-cli/sessions", map[string]any{
		"text": "not dispatched", "clientId": payloadSession, "nativeScopeId": "scope_a",
	}, nil)
	if status != 501 || len(chain.writes()) != 0 || chain.native.dispatches != 0 {
		t.Fatalf("read-only source admitted create: %d", status)
	}
}
