package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFourConfiguredAdaptersPersistWithoutNativeScopes(t *testing.T) {
	ids := []string{codexDesktopSource, codexCLISource, claudeCodeSource, claudeDesktopSource}
	adapters := make([]Adapter, 0, len(ids))
	for _, id := range ids {
		family, origin := "codex", "cli"
		if id == claudeCodeSource || id == claudeDesktopSource {
			family = "claude"
		}
		if id == codexDesktopSource || id == claudeDesktopSource {
			origin = "desktop"
		}
		adapters = append(adapters, Adapter{ID: id, Family: family, Origin: origin,
			Scope: func(context.Context) (string, error) { return "", errors.New("native scope unavailable") }})
	}
	_, base, client := encryptedAdapterServer(t, adapters, nil)
	status, data := adapterRequest(t, client, http.MethodGet, base+"/v1/info", nil, nil)
	var info infoResponse
	if status != http.StatusOK || json.Unmarshal(data, &info) != nil || len(info.Adapters) != len(ids) {
		t.Fatalf("four-source discovery: %d %s", status, data)
	}
	seen := map[string]bool{}
	for i, descriptor := range info.Adapters {
		if descriptor.ID != ids[i] || descriptor.State != "unavailable" || descriptor.NativeScopeID == "" || seen[descriptor.NativeScopeID] {
			t.Fatalf("missing or reused unavailable identity: %+v", descriptor)
		}
		seen[descriptor.NativeScopeID] = true
		status, _ := adapterRequest(t, client, http.MethodGet, base+"/v1/adapters/"+descriptor.ID+"/sessions", nil, nil)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("unavailable source %s route: %d", descriptor.ID, status)
		}
	}
}

func TestSelectedGatewaySourcesPersistWhenWorkerIsUnavailable(t *testing.T) {
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer private.Close()
	worker, err := NewUserWorker(private.URL, StaticUserWorkerToken(testToken), nil, "", DefaultUserWorkerTimeouts)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	ids := []string{codexCLISource, claudeCodeSource, claudeDesktopSource}
	adapters := gatewaySourceAdapters(ids, worker, func(string, string) {}, Adapter{}, nil)
	_, base, client := encryptedAdapterServer(t, adapters, nil)
	status, data := adapterRequest(t, client, http.MethodGet, base+"/v1/info", nil, nil)
	var info infoResponse
	if status != http.StatusOK || json.Unmarshal(data, &info) != nil || len(info.Adapters) != len(ids) {
		t.Fatalf("offline worker hid configured sources: %d %s", status, data)
	}
	for i, descriptor := range info.Adapters {
		if descriptor.ID != ids[i] || descriptor.State != "unavailable" || descriptor.NativeScopeID == "" {
			t.Fatalf("offline descriptor: %+v", descriptor)
		}
		if descriptor.Capabilities.Create || descriptor.Capabilities.IdleSend || descriptor.Capabilities.Stop || descriptor.Capabilities.Uploads {
			t.Fatalf("observation source acquired control: %+v", descriptor)
		}
	}
	if info.Adapters[2].Origin != "desktop" || info.Adapters[1].Origin != "cli" {
		t.Fatal("Claude native origins were merged")
	}
	status, _ = adapterRequest(t, client, http.MethodGet, base+"/v1/adapters/codex-desktop/sessions", nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("unselected source became available: %d", status)
	}
}

func TestClaudeDesktopOnlyValidatesPrivateBridgeEndpoint(t *testing.T) {
	for _, id := range []string{claudeCodeSource, claudeDesktopSource} {
		if err := validateSourceEndpoints(map[string]any{"claudeBridgePort": float64(23456)}, []string{id}, 23456); err == nil {
			t.Fatalf("%s shared worker port", id)
		}
		if err := validateSourceEndpoints(map[string]any{}, []string{id}, 23456); err != nil {
			t.Fatalf("%s default endpoint: %v", id, err)
		}
	}
}
