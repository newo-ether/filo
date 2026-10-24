package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestAdapterCapabilitiesTrackFreshEvidenceAndNeverExceedCeiling(t *testing.T) {
	native := newAdapterFixture()
	adapter := fixtureAdapter(codexCLISource, native, nil)
	adapter.Capabilities = sourceHistoryCaps(false)
	adapter.Capabilities.Models, adapter.Capabilities.Usage = true, true
	usageCalls := 0
	adapter.Usage = func(context.Context) (protocol.AccountUsage, error) {
		usageCalls++
		return protocol.AccountUsage{}, nil
	}
	current := sourceHistoryCaps(false)
	var cause error
	adapter.CapabilitySource = func(context.Context) (protocol.AdapterCapabilities, error) {
		return current, cause
	}
	_, base, client := encryptedAdapterServer(t, []Adapter{adapter}, nil)
	info := func() protocol.AdapterDescriptor {
		t.Helper()
		status, data := adapterRequest(t, client, http.MethodGet, base+"/v1/info", nil, nil)
		var response infoResponse
		if status != http.StatusOK || json.Unmarshal(data, &response) != nil || len(response.Adapters) != 1 {
			t.Fatalf("discovery: %d", status)
		}
		return response.Adapters[0]
	}
	path := base + "/v1/adapters/codex-cli/"
	if value := info(); value.Capabilities.Models || value.Capabilities.Usage {
		t.Fatal("unconfigured metadata was advertised")
	}
	for _, route := range []string{"models", "usage"} {
		status, _ := adapterRequest(t, client, http.MethodGet, path+route, nil, nil)
		if status != http.StatusNotImplemented {
			t.Fatalf("unconfigured %s route: %d", route, status)
		}
	}
	current = fixtureCaps()
	value := info()
	if !value.Capabilities.Models || !value.Capabilities.Usage || value.Capabilities.Create ||
		value.Capabilities.IdleSend || value.Capabilities.Stop || value.Capabilities.ActiveSend != "unsupported" {
		t.Fatal("fresh metadata was missing or exceeded its read-only ceiling")
	}
	for _, route := range []string{"models", "usage"} {
		status, _ := adapterRequest(t, client, http.MethodGet, path+route, nil, nil)
		if status != http.StatusOK {
			t.Fatalf("configured %s route: %d", route, status)
		}
	}
	if usageCalls != 1 {
		t.Fatal("usage dispatch was missing or replayed")
	}
	cause = errors.New("native discovery disconnected")
	value = info()
	if value.State != "unavailable" || value.Capabilities.Models || value.Capabilities.Usage || value.Capabilities.History {
		t.Fatal("failed capability discovery retained earlier permissions")
	}
	status, _ := adapterRequest(t, client, http.MethodGet, path+"usage", nil, nil)
	if status != http.StatusServiceUnavailable || usageCalls != 1 {
		t.Fatal("failed discovery reached native usage")
	}
	cause = nil
	current.ActiveSend = "invented-native-mode"
	if value = info(); value.State != "unavailable" || value.Capabilities.Models {
		t.Fatal("malformed capability evidence was accepted")
	}
}
