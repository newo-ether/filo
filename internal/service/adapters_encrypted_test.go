package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/conch/encryptedhttp"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/uploads"
)

type adapterFixture struct {
	*fakeSessions
	state                   sync.Mutex
	scope, binding          string
	accessCalls, dispatches int
	beforeDispatch          func()
}

func newAdapterFixture() *adapterFixture {
	f := &adapterFixture{fakeSessions: newFakeSessions(2), scope: "scope_a", binding: "binding_a"}
	f.sessions = []protocol.Session{{ID: payloadSession}}
	return f
}

func (f *adapterFixture) scopeID(context.Context) (string, error) {
	f.state.Lock()
	defer f.state.Unlock()
	return f.scope, nil
}

func (f *adapterFixture) rotate(scope, binding string) {
	f.state.Lock()
	f.scope, f.binding = scope, binding
	f.state.Unlock()
}

func (f *adapterFixture) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	f.state.Lock()
	defer f.state.Unlock()
	f.accessCalls++
	return protocol.SessionAccess{NativeScopeID: f.scope, CanonicalSessionRef: "canonical_session", Binding: f.binding, Capabilities: fixtureCaps()}, nil
}

func (f *adapterFixture) ValidateAccess(ctx context.Context, id, binding string) error {
	if err := protocol.ValidateScope(ctx); err != nil {
		return err
	}
	f.state.Lock()
	defer f.state.Unlock()
	if binding != f.binding {
		return errors.New("stale native binding")
	}
	return nil
}

func (f *adapterFixture) dispatch(ctx context.Context, id string) error {
	if f.beforeDispatch != nil {
		f.beforeDispatch()
	}
	if err := f.ValidateAccess(ctx, id, protocol.AccessBinding(ctx)); err != nil {
		return err
	}
	f.state.Lock()
	f.dispatches++
	f.state.Unlock()
	return nil
}

func (f *adapterFixture) Send(ctx context.Context, id, text, client string) (protocol.SendReceipt, error) {
	if err := f.dispatch(ctx, id); err != nil {
		return protocol.SendReceipt{}, err
	}
	return f.fakeSessions.Send(ctx, id, text, client)
}

func (f *adapterFixture) UpdateSettings(ctx context.Context, id string, settings protocol.SessionSettings) error {
	if err := f.dispatch(ctx, id); err != nil {
		return err
	}
	return f.fakeSessions.UpdateSettings(ctx, id, settings)
}

func (f *adapterFixture) Create(ctx context.Context, text, client string, settings protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	if f.beforeDispatch != nil {
		f.beforeDispatch()
	}
	if err := protocol.ValidateScope(ctx); err != nil {
		return protocol.Session{}, protocol.SendReceipt{}, err
	}
	f.state.Lock()
	f.dispatches++
	f.state.Unlock()
	return f.fakeSessions.Create(ctx, text, client, settings)
}

func fixtureCaps() protocol.AdapterCapabilities {
	return protocol.AdapterCapabilities{Create: true, History: true, Live: true, IdleSend: true, ActiveSend: "native-steer", Stop: true,
		Settings: []string{"model", "effort", "serviceTier"}, Rename: true, Archive: true, Uploads: true, Models: true, Usage: true, LazyMessages: true, HistoryMode: "bounded"}
}

func fixtureAdapter(id string, f *adapterFixture, store *uploads.Store) Adapter {
	return Adapter{ID: id, Family: "codex", Origin: "cli", Scope: f.scopeID, Sessions: f, Uploads: store, Capabilities: fixtureCaps(),
		Status: func() AdapterStatus { return AdapterStatus{State: "connected"} }}
}

func encryptedAdapterServer(t *testing.T, adapters []Adapter, legacy SessionAPI) (*Server, string, *http.Client) {
	t.Helper()
	gateway, err := NewPublicServer(ServerOptions{Token: testToken, Sessions: legacy, Adapters: adapters})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(gateway)
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 3 * time.Second, Transport: &encryptedhttp.Transport{Key: []byte(testToken)}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return gateway.Server, server.URL, client
}

func adapterRequest(t *testing.T, client *http.Client, method, path string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if raw, ok := body.([]byte); ok {
		reader = bytes.NewReader(raw)
	} else if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

func fixtureBinding(t *testing.T, client *http.Client, base, id string) string {
	t.Helper()
	status, data := adapterRequest(t, client, "GET", base+"/v1/adapters/"+id+"/sessions/"+payloadSession, nil, nil)
	var page protocol.ConversationPage
	if status != 200 || json.Unmarshal(data, &page) != nil || page.Access == nil {
		t.Fatalf("page %d %s", status, data)
	}
	return page.Access.Binding
}

func TestAdapterEncryptedBodyBindingDispatchesExactlyOnceAndRejectsStale(t *testing.T) {
	native := newAdapterFixture()
	_, base, client := encryptedAdapterServer(t, []Adapter{fixtureAdapter("codex-cli", native, nil)}, nil)
	binding := fixtureBinding(t, client, base, "codex-cli")
	path := base + "/v1/adapters/codex-cli/sessions/" + payloadSession + "/messages"
	body := map[string]any{"text": "one native input", "clientId": payloadSession, "accessBinding": binding}
	status, data := adapterRequest(t, client, "POST", path, body, nil)
	if status != 202 {
		t.Fatalf("send %d %s", status, data)
	}
	native.rotate("scope_a", "binding_b")
	status, _ = adapterRequest(t, client, "POST", path, body, nil)
	if status < 400 {
		t.Fatal("stale binding accepted")
	}
	delete(body, "accessBinding")
	status, _ = adapterRequest(t, client, "POST", path, body, map[string]string{"X-Filo-Access-Binding": binding})
	if status < 400 {
		t.Fatal("header-only binding accepted")
	}
	if native.dispatches != 1 {
		t.Fatalf("native dispatches %d", native.dispatches)
	}
}

func TestAdapterEncryptedSettingsStripsBindingBeforeNativeParsing(t *testing.T) {
	native := newAdapterFixture()
	_, base, client := encryptedAdapterServer(t, []Adapter{fixtureAdapter("codex-cli", native, nil)}, nil)
	binding := fixtureBinding(t, client, base, "codex-cli")
	status, data := adapterRequest(t, client, "POST", base+"/v1/adapters/codex-cli/sessions/"+payloadSession+"/settings", map[string]any{"model": "fixture-model", "accessBinding": binding}, nil)
	if status != 200 || native.dispatches != 1 || len(native.settings) != 1 || native.settings[0].Model.Value != "fixture-model" {
		t.Fatalf("settings %d %s dispatch=%d", status, data, native.dispatches)
	}
}

func TestAdapterRefusesSourceAndScopeTransferAtFinalDispatch(t *testing.T) {
	first, second := newAdapterFixture(), newAdapterFixture()
	_, base, client := encryptedAdapterServer(t, []Adapter{fixtureAdapter("codex-desktop", first, nil), fixtureAdapter("codex-cli", second, nil)}, first)
	binding := fixtureBinding(t, client, base, "codex-desktop")
	body := map[string]any{"text": "input", "clientId": payloadSession, "accessBinding": binding}
	status, _ := adapterRequest(t, client, "POST", base+"/v1/adapters/codex-cli/sessions/"+payloadSession+"/messages", body, nil)
	if status < 400 || second.dispatches != 0 {
		t.Fatal("binding crossed access path")
	}
	first.beforeDispatch = func() { first.rotate("scope_b", "binding_a") }
	status, _ = adapterRequest(t, client, "POST", base+"/v1/adapters/codex-desktop/sessions/"+payloadSession+"/messages", body, nil)
	if status < 400 || first.dispatches != 0 {
		t.Fatal("scope changed at native dispatch")
	}
}

func TestAdapterCreateRequiresCapturedScopeAndRechecksDispatch(t *testing.T) {
	native := newAdapterFixture()
	_, base, client := encryptedAdapterServer(t, []Adapter{fixtureAdapter("codex-cli", native, nil)}, nil)
	path := base + "/v1/adapters/codex-cli/sessions"
	body := map[string]any{"text": "new session", "clientId": payloadSession, "nativeScopeId": "scope_old"}
	status, _ := adapterRequest(t, client, "POST", path, body, nil)
	if status < 400 || native.dispatches != 0 {
		t.Fatal("old draft admitted")
	}
	body["nativeScopeId"] = "scope_a"
	native.beforeDispatch = func() { native.rotate("scope_b", "binding_b") }
	status, _ = adapterRequest(t, client, "POST", path, body, nil)
	if status < 400 || native.dispatches != 0 {
		t.Fatal("create crossed native account")
	}
	native.beforeDispatch = nil
	body["nativeScopeId"] = "scope_b"
	status, data := adapterRequest(t, client, "POST", path, body, nil)
	if status != 201 || native.dispatches != 1 {
		t.Fatalf("create %d %s", status, data)
	}
}

func TestAdapterUploadsAreScopeSealedAndShareOneBudget(t *testing.T) {
	store, err := uploads.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, second := newAdapterFixture(), newAdapterFixture()
	root, base, client := encryptedAdapterServer(t, []Adapter{fixtureAdapter("codex-desktop", first, store), fixtureAdapter("codex-cli", second, store)}, first)
	if root.adapters["codex-cli"].root.uploads != store {
		t.Fatal("upload budget not shared")
	}
	digest := sha256.Sum256([]byte("hello"))
	meta := map[string]any{"name": "fixture.txt", "mime": "text/plain", "size": 5, "sha256": hex.EncodeToString(digest[:]), "nativeScopeId": "scope_a"}
	status, data := adapterRequest(t, client, "POST", base+"/v1/adapters/codex-desktop/uploads", meta, nil)
	var uploaded uploads.Metadata
	if status != 201 || json.Unmarshal(data, &uploaded) != nil || !strings.HasPrefix(uploaded.ID, "filo.scope.") {
		t.Fatalf("begin %d %s", status, data)
	}
	path := "/uploads/" + url.PathEscape(uploaded.ID)
	status, _ = adapterRequest(t, client, "PUT", base+"/v1/adapters/codex-cli"+path+"?offset=0", []byte("hello"), nil)
	if status < 400 {
		t.Fatal("upload crossed source")
	}
	status, data = adapterRequest(t, client, "PUT", base+"/v1/adapters/codex-desktop"+path+"?offset=0", []byte("hello"), nil)
	if status != 200 {
		t.Fatalf("append %d %s", status, data)
	}
	status, data = adapterRequest(t, client, "POST", base+"/v1/adapters/codex-desktop"+path+"/complete", map[string]any{}, nil)
	if status != 200 || bytes.Contains(data, []byte(`"path"`)) {
		t.Fatalf("complete %d %s", status, data)
	}
	first.rotate("scope_b", "binding_b")
	status, _ = adapterRequest(t, client, "DELETE", base+"/v1/adapters/codex-desktop"+path, nil, nil)
	if status < 400 {
		t.Fatal("upload token survived native scope change")
	}
}
