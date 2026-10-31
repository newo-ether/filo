package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func discoveryFixture(t *testing.T) (*Adapter, string) {
	t.Helper()
	a := nativeOriginFixture(t, "desktop")
	return a, registerOriginFixture(t, a, "desktop", fixtureSession)
}

func TestDesktopDiscoveryIsPrivateReadOnlyAndRegistrationBound(t *testing.T) {
	a, nonce := discoveryFixture(t)
	before := a.Descriptor().Capabilities
	query := func(want string) {
		t.Helper()
		status, result := bridgeCall(t, a, "discovery", envelope{SessionID: fixtureSession, Nonce: nonce})
		if status != http.StatusOK || !result.ReadOnly || result.Discovery == nil || result.Discovery.Status != want {
			t.Fatalf("discovery query: %d %+v", status, result)
		}
	}
	query("not-observed")
	event := modEvent{Kind: "discovery", Discovery: &desktopDiscovery{Status: "unverified", Tools: []string{"mcp__ccd_session_mgmt__get_session"}}}
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1, Events: []modEvent{event}})
	if status != http.StatusOK {
		t.Fatal("bounded discovery was refused")
	}
	query("unverified")
	if !reflect.DeepEqual(a.Descriptor().Capabilities, before) {
		t.Fatal("discovery promoted source capabilities")
	}
	page, err := a.Read(context.Background(), fixtureSession, "", false, nil)
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("discovery contaminated conversation/runtime: %+v %v", page, err)
	}
	a.mu.Lock()
	r := a.registrations[fixtureSession]
	sequence, expires := r.sequence, r.expires
	copy := cloneDesktopDiscovery(r.discovery)
	copy.Tools[0] = "changed"
	if r.discovery.Tools[0] != event.Discovery.Tools[0] {
		t.Fatal("discovery aliases mutable slices")
	}
	a.mu.Unlock()
	query("unverified")
	if r.sequence != sequence || !r.expires.Equal(expires) {
		t.Fatal("diagnostic query refreshed registration or event sequence")
	}
	old := nonce
	nonce = registerOriginFixture(t, a, "desktop", fixtureSession)
	query("not-observed")
	status, _ = bridgeCall(t, a, "discovery", envelope{SessionID: fixtureSession, Nonce: old})
	if status != http.StatusConflict {
		t.Fatal("old nonce read replacement discovery")
	}
	status, _ = bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: old, Sequence: 1, Events: []modEvent{event}})
	if status != http.StatusConflict {
		t.Fatal("late old observation entered replacement registration")
	}
	a.now = func() time.Time { return expires.Add(time.Second) }
	status, _ = bridgeCall(t, a, "discovery", envelope{SessionID: fixtureSession, Nonce: nonce})
	if status != http.StatusConflict {
		t.Fatal("expired registration retained discovery")
	}
}

func TestDesktopDiscoveryRejectsForgedEvidenceAtomically(t *testing.T) {
	valid := desktopDiscovery{Status: "unverified", Tools: []string{"mcp__ccd_session_mgmt__get_session"}}
	for name, event := range map[string]modEvent{
		"trusted-status":       {Kind: "discovery", Discovery: &desktopDiscovery{Status: "verified", Tools: valid.Tools}},
		"unknown-tool":         {Kind: "discovery", Discovery: &desktopDiscovery{Status: "unverified", Tools: []string{"mcp__other__get_session"}}},
		"other-session-server": {Kind: "discovery", Discovery: &desktopDiscovery{Status: "unverified", Tools: []string{"mcp__ccd_session__get_session"}}},
		"duplicate":            {Kind: "discovery", Discovery: &desktopDiscovery{Status: "unverified", Tools: append(valid.Tools, valid.Tools...)}},
		"failed-retains-tools": {Kind: "discovery", Discovery: &desktopDiscovery{Status: "timeout", Tools: valid.Tools}},
		"null-tools":           {Kind: "discovery", Discovery: &desktopDiscovery{Status: "unverified"}},
		"missing-evidence":     {Kind: "discovery"},
		"runtime-field":        {Kind: "discovery", Discovery: &valid, Model: stringPointer("model")},
		"other-kind":           {Kind: "busy", Discovery: &valid},
	} {
		t.Run(name, func(t *testing.T) {
			a, nonce := discoveryFixture(t)
			status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1,
				Events: []modEvent{{Kind: "row", UUID: fixtureRow, Role: "user", Text: "must roll back"}, event}})
			if status != http.StatusConflict || a.registrations[fixtureSession].sequence != 0 || len(a.registrations[fixtureSession].rows) != 0 {
				t.Fatal("invalid evidence partially committed")
			}
		})
	}
}

func TestDesktopDiscoveryQueryRejectsMutatingOrTerminalEnvelope(t *testing.T) {
	a, nonce := discoveryFixture(t)
	for _, input := range []envelope{
		{SessionID: fixtureSession, Nonce: nonce, Sequence: 1},
		{SessionID: fixtureSession, Nonce: nonce, Events: []modEvent{}},
		{SessionID: fixtureSession, Nonce: nonce, Result: &modCommandResult{}},
	} {
		// omitempty does not preserve an empty event array; test its raw wire form below.
		if input.Events != nil {
			continue
		}
		status, _ := bridgeCall(t, a, "discovery", input)
		if status != http.StatusConflict {
			t.Fatal("discovery accepted a mutation envelope")
		}
	}
	terminal := fixtureAdapter(t)
	tnonce := registerFixture(t, terminal, fixtureSession)
	event := modEvent{Kind: "discovery", Discovery: &desktopDiscovery{Status: "unverified", Tools: []string{}}}
	for action, input := range map[string]envelope{
		"discovery": {SessionID: fixtureSession, Nonce: tnonce},
		"push":      {SessionID: fixtureSession, Nonce: tnonce, Sequence: 1, Events: []modEvent{event}},
	} {
		status, _ := bridgeCall(t, terminal, action, input)
		if status != http.StatusConflict {
			t.Fatal("terminal origin admitted Desktop evidence")
		}
	}
	for _, fields := range []string{`,"events":[]`, `,"discovery":{"status":"verified"}`, `,"tools":[]`} {
		body := `{"sessionId":"` + fixtureSession + `","nonce":"` + nonce + `"` + fields + `}`
		request, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1"+bridgePrefix+"discovery", strings.NewReader(body))
		var decoded envelope
		err := decodeRequest(request, &decoded)
		if fields == `,"events":[]` {
			if err != nil || decoded.Events == nil {
				t.Fatal("raw empty array was lost before strict query validation")
			}
		} else if err == nil {
			t.Fatal("unrecognized discovery input was accepted")
		}
	}
	encoded, _ := json.Marshal(cloneDesktopDiscovery(nil))
	if string(encoded) != `{"status":"not-observed","tools":[]}` {
		t.Fatalf("missing evidence must remain explicit: %s", encoded)
	}
}

func stringPointer(value string) *string { return &value }
