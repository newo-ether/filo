package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

type catalogManagementFixture struct{ *adapterFixture }

func (f *catalogManagementFixture) CatalogAccess(ctx context.Context, id string) (protocol.SessionAccess, error) {
	f.state.Lock()
	defer f.state.Unlock()
	return protocol.SessionAccess{NativeScopeID: f.scope, CanonicalSessionRef: "canonical_session",
		Binding: "catalog_" + f.binding, Capabilities: protocol.AdapterCapabilities{
			History: true, Rename: true, Archive: true, ActiveSend: "unsupported", Settings: []string{}, HistoryMode: "bounded"}}, nil
}

func (f *catalogManagementFixture) ValidateAccess(ctx context.Context, id, binding string) error {
	access, err := f.CatalogAccess(ctx, id)
	if err == nil && binding == access.Binding {
		return protocol.ValidateScope(ctx)
	}
	return f.adapterFixture.ValidateAccess(ctx, id, binding)
}

func (f *catalogManagementFixture) Rename(ctx context.Context, id, name string) (protocol.Session, error) {
	if err := f.ValidateAccess(ctx, id, protocol.AccessBinding(ctx)); err != nil {
		return protocol.Session{}, err
	}
	f.state.Lock()
	f.dispatches++
	f.state.Unlock()
	return protocol.Session{ID: id, Title: protocol.Text(name)}, nil
}

func (f *catalogManagementFixture) Archive(ctx context.Context, id string) (string, error) {
	if err := f.ValidateAccess(ctx, id, protocol.AccessBinding(ctx)); err != nil {
		return "", err
	}
	f.state.Lock()
	f.dispatches++
	f.state.Unlock()
	return "fixture", nil
}

func TestCatalogManagementUsesExactAccountBindingWithoutExecutionAccess(t *testing.T) {
	fixture := &catalogManagementFixture{newAdapterFixture()}
	adapter := fixtureAdapter("codex-desktop", fixture.adapterFixture, nil)
	adapter.Sessions = fixture
	_, base, client := encryptedAdapterServer(t, []Adapter{adapter}, fixture)
	path := base + "/v1/adapters/codex-desktop/sessions"
	status, data := adapterRequest(t, client, "GET", path, nil, nil)
	var page protocol.SessionPage
	if status != http.StatusOK || json.Unmarshal(data, &page) != nil || len(page.Sessions) != 1 {
		t.Fatalf("catalog: %d %s", status, data)
	}
	access := page.Sessions[0].Access
	if access == nil || !access.Capabilities.Rename || !access.Capabilities.Archive || access.Capabilities.IdleSend {
		t.Fatal("catalog management grant is missing or grants execution", access)
	}
	for _, action := range []string{"rename", "archive", "messages", "stop"} {
		status, _ = adapterRequest(t, client, "POST", path+"/"+payloadSession+"/"+action,
			map[string]any{"accessBinding": access.Binding, "name": "New title", "text": "must not run", "clientId": "client", "turnId": "turn"}, nil)
		if (status < 300) != (action == "rename" || action == "archive") {
			t.Fatalf("catalog %s status = %d", action, status)
		}
	}
	if fixture.accessCalls != 2 || fixture.dispatches != 2 {
		t.Fatalf("native execution reads/management dispatches = %d/%d", fixture.accessCalls, fixture.dispatches)
	}
	fixture.rotate("scope_b", "binding_b")
	status, _ = adapterRequest(t, client, "POST", path+"/"+payloadSession+"/rename",
		map[string]any{"accessBinding": access.Binding, "name": "stale"}, nil)
	if status < 400 || fixture.dispatches != 2 {
		t.Fatal("stale native account granted metadata mutation", status)
	}
}
