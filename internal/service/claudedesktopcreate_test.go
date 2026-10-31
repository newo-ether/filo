package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/newo-ether/filo/internal/claude"
	"github.com/newo-ether/filo/internal/protocol"
)

type desktopCreateBoundary struct {
	*claude.Adapter
	creates atomic.Int32
}

func (s *desktopCreateBoundary) Create(ctx context.Context, text, client string, settings protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	s.creates.Add(1)
	return s.Adapter.Create(ctx, text, client, settings)
}

func TestClaudeDesktopCreationRemainsUnsupportedAcrossWorkerAndEncryptedGateway(t *testing.T) {
	mod, err := claude.New(claude.Config{Token: testToken, NativeScopeID: "desktop-create-scope", Origin: "desktop"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/claude-mod/v1/register", strings.NewReader(
		`{"sessionId":"11111111-1111-4111-8111-111111111111","instance":"create-boundary","origin":"desktop"}`))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Authorization", "Bearer "+testToken)
	registered := httptest.NewRecorder()
	claude.SharedBridge(mod).ServeHTTP(registered, request)
	if registered.Code != http.StatusOK {
		t.Fatalf("Mod fixture registration: %d", registered.Code)
	}
	native := &desktopCreateBoundary{Adapter: mod}
	private, err := NewServer(ServerOptions{Token: testToken, Adapters: []Adapter{{
		ID: claudeDesktopSource, Family: "claude", Origin: "desktop", NativeScopeID: "desktop-create-scope",
		Sessions: native, Capabilities: claude.Capabilities(),
		Status: func() AdapterStatus { return AdapterStatus{State: mod.Status().State} },
	}}})
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
	legacy := newAdapterFixture()
	adapters := gatewaySourceAdapters([]string{claudeDesktopSource}, worker, func(string, string) {}, Adapter{}, nil)
	_, base, client := encryptedAdapterServer(t, adapters, legacy)
	for _, target := range []struct {
		name, base string
		client     *http.Client
		headers    map[string]string
	}{
		{"worker", endpoint.URL, endpoint.Client(), map[string]string{"Authorization": "Bearer " + testToken}},
		{"encrypted-gateway", base, client, nil},
	} {
		t.Run(target.name, func(t *testing.T) {
			status, data := adapterRequest(t, target.client, http.MethodGet, target.base+"/v1/info", nil, target.headers)
			var info infoResponse
			if status != http.StatusOK || json.Unmarshal(data, &info) != nil || len(info.Adapters) != 1 {
				t.Fatalf("discovery status=%d", status)
			}
			source := info.Adapters[0]
			if source.ID != claudeDesktopSource || source.State != "connected" || source.Capabilities.Create || source.Capabilities.Models || len(source.Capabilities.Settings) != 1 || source.Capabilities.Settings[0] != "effort" {
				t.Fatalf("Desktop creation boundary: %+v", source)
			}
			status, _ = adapterRequest(t, target.client, http.MethodGet, target.base+"/v1/adapters/claude-desktop/models", nil, target.headers)
			if status != http.StatusNotImplemented {
				t.Fatalf("unsupported Desktop model catalog status=%d", status)
			}
			status, _ = adapterRequest(t, target.client, http.MethodPost, target.base+"/v1/adapters/claude-desktop/sessions",
				map[string]any{"nativeScopeId": source.NativeScopeID, "text": "first message", "clientId": "create-boundary"}, target.headers)
			if status != http.StatusNotImplemented || native.creates.Load() != 0 || legacy.dispatches != 0 {
				t.Fatalf("unsupported creation status=%d native=%d legacy=%d", status, native.creates.Load(), legacy.dispatches)
			}
			t.Logf("connected Desktop: create=false, POST sessions=%d, native creates=0, fallback dispatches=0", status)
		})
	}
	page, err := mod.List(context.Background(), "")
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatal("unsupported creation changed the registered Mod catalog")
	}
}
