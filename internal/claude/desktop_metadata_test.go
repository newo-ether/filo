package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func startMetadataHTTP(t *testing.T, a *Adapter, nonce string) (<-chan *httptest.ResponseRecorder, context.CancelFunc) {
	t.Helper()
	desktopIdentityFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	body, err := json.Marshal(envelope{SessionID: fixtureSession, Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, bridgePrefix+"metadata", strings.NewReader(string(body))).WithContext(ctx)
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Authorization", "Bearer "+fixtureToken)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		writer := httptest.NewRecorder()
		a.ServeHTTP(writer, request)
		done <- writer
	}()
	pendingStop(t, a)
	return done, cancel
}

func metadataHTTPResult(t *testing.T, done <-chan *httptest.ResponseRecorder) (int, bridgeResponse) {
	t.Helper()
	select {
	case writer := <-done:
		var response bridgeResponse
		if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return writer.Code, response
	case <-time.After(5 * time.Second):
		t.Fatal("bounded metadata request did not finish")
		return 0, bridgeResponse{}
	}
}

func pullMetadata(t *testing.T, a *Adapter, nonce string) string {
	t.Helper()
	status, response := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
	if status != http.StatusOK || len(response.Commands) != 1 {
		t.Fatalf("metadata command not delivered: %d %+v", status, response)
	}
	command := response.Commands[0].(map[string]any)
	id, _ := command["id"].(string)
	if len(command) != 3 || command["hostSessionId"] != fixtureHostSession || command["operation"] != "desktop-metadata" || !opaque.MatchString(id) {
		t.Fatalf("metadata command accepted caller parameters: %+v", command)
	}
	_, again := bridgeCall(t, a, "pull", envelope{SessionID: fixtureSession, Nonce: nonce})
	if len(again.Commands) != 0 {
		t.Fatal("metadata command was delivered twice")
	}
	return id
}

func TestDesktopMetadataExplicitReadbackDoesNotPromoteSession(t *testing.T) {
	a, nonce := discoveryFixture(t)
	capabilities := a.Descriptor().Capabilities
	r := a.registrations[fixtureSession]
	title, expires, sequence := r.title, r.expires, r.sequence
	discovery := cloneDesktopDiscovery(r.discovery)
	done, _ := startMetadataHTTP(t, a, nonce)
	a.mu.Lock()
	untouched := r.title == title && r.expires.Equal(expires) && r.sequence == sequence
	a.mu.Unlock()
	if !untouched {
		t.Fatal("query itself changed registration projection or lease")
	}
	status, _ := bridgeCall(t, a, "metadata", envelope{SessionID: fixtureSession, Nonce: nonce})
	if status != http.StatusConflict {
		t.Fatal("concurrent metadata query replaced the pending command")
	}
	id := pullMetadata(t, a, nonce)
	status, _ = bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce,
		Result: &modCommandResult{ID: id, OK: true, Metadata: &desktopMetadata{
			Status: "matched", SessionID: fixtureSession, HostSessionID: fixtureHostSession, Title: stringPointer("native readback")}}})
	if status != http.StatusOK {
		t.Fatal("matching native readback refused")
	}
	status, result := metadataHTTPResult(t, done)
	if status != http.StatusOK || !result.ReadOnly || result.Metadata == nil || result.Metadata.Status != "matched" ||
		result.Metadata.SessionID != fixtureSession || result.Metadata.Title == nil || *result.Metadata.Title != "native readback" {
		t.Fatalf("readback response: %d %+v", status, result)
	}
	if !reflect.DeepEqual(a.Descriptor().Capabilities, capabilities) || r.title != title || r.sequence != sequence ||
		!reflect.DeepEqual(cloneDesktopDiscovery(r.discovery), discovery) || r.pending != nil {
		t.Fatal("metadata changed capabilities, title, events or discovery")
	}
	page, err := a.Read(context.Background(), fixtureSession, "", false, nil)
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("metadata contaminated conversation history: %+v %v", page, err)
	}
	status, _ = bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce,
		Result: &modCommandResult{ID: id, OK: true, Metadata: result.Metadata}})
	if status != http.StatusConflict {
		t.Fatal("released query accepted a late result")
	}
}

func TestDesktopMetadataHTTPRejectsUnboundOrMutatingQueries(t *testing.T) {
	a, nonce := discoveryFixture(t)
	for _, fields := range []string{`,"sequence":1`, `,"events":[]`, `,"result":{}`, `,"tool":"other"`, ` ,"hostSessionId":"local_spoofed"`, `,"session_id":"self"`} {
		request := httptest.NewRequest(http.MethodPost, bridgePrefix+"metadata", strings.NewReader(
			`{"sessionId":"`+fixtureSession+`","nonce":"`+nonce+`"`+fields+`}`))
		request.RemoteAddr = "127.0.0.1:1"
		request.Header.Set("Authorization", "Bearer "+fixtureToken)
		writer := httptest.NewRecorder()
		a.ServeHTTP(writer, request)
		if writer.Code != http.StatusConflict && writer.Code != http.StatusBadRequest {
			t.Fatalf("query accepted mutation/argument %s: %d", fields, writer.Code)
		}
	}
	status, _ := bridgeCall(t, a, "metadata", envelope{SessionID: fixtureSession, Nonce: strings.Repeat("z", 43)})
	if status != http.StatusConflict || a.registrations[fixtureSession].pending != nil {
		t.Fatal("stale nonce queued metadata")
	}
	terminal := fixtureAdapter(t)
	tnonce := registerFixture(t, terminal, fixtureSession)
	status, _ = bridgeCall(t, terminal, "metadata", envelope{SessionID: fixtureSession, Nonce: tnonce})
	if status != http.StatusConflict || terminal.registrations[fixtureSession].pending != nil {
		t.Fatal("terminal origin queued Desktop metadata")
	}
	for _, tc := range []struct{ remote, token, origin string }{
		{"192.0.2.1:1", fixtureToken, ""}, {"127.0.0.1:1", "wrong", ""},
		{"127.0.0.1:1", fixtureToken, "https://example.com"},
	} {
		request := httptest.NewRequest(http.MethodPost, bridgePrefix+"metadata", strings.NewReader(`{}`))
		request.RemoteAddr = tc.remote
		request.Header.Set("Authorization", "Bearer "+tc.token)
		request.Header.Set("Origin", tc.origin)
		writer := httptest.NewRecorder()
		a.ServeHTTP(writer, request)
		if writer.Code != http.StatusUnauthorized {
			t.Fatalf("metadata bypassed private bridge authentication: %+v", tc)
		}
	}
}

func TestDesktopMetadataCancellationTimeoutAndRebindDoNotReplay(t *testing.T) {
	for _, action := range []string{"cancel", "timeout", "rebind"} {
		t.Run(action, func(t *testing.T) {
			a, nonce := discoveryFixture(t)
			done, cancel := startMetadataHTTP(t, a, nonce)
			id := pullMetadata(t, a, nonce)
			if action == "cancel" {
				cancel()
			}
			if action == "rebind" {
				registerOriginFixture(t, a, "desktop", fixtureSession)
			}
			status, response := metadataHTTPResult(t, done)
			if action == "rebind" {
				if status != http.StatusConflict || !strings.Contains(response.Error, "unknown") {
					t.Fatalf("rebind did not disclose uncertain outcome: %d %+v", status, response)
				}
			} else if status != http.StatusOK || response.Metadata == nil || response.Metadata.Status != "unknown" ||
				response.Metadata.SessionID != "" || response.Metadata.Title != nil {
				t.Fatalf("canceled/expired query retained evidence: %d %+v", status, response)
			}
			status, _ = bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce,
				Result: &modCommandResult{ID: id, OK: true, Metadata: &desktopMetadata{Status: "matched", SessionID: fixtureSession}}})
			if status != http.StatusConflict || a.registrations[fixtureSession].pending != nil {
				t.Fatal("late metadata crossed a query or registration lifetime")
			}
		})
	}
}

func TestDesktopMetadataResultValidationAndDuplicateIdentity(t *testing.T) {
	a, nonce := discoveryFixture(t)
	// Retain the dispatched command so duplicate result handling has no waiter race.
	r := a.registrations[fixtureSession]
	id := strings.Repeat("m", 43)
	r.pending = &pendingModCommand{command: modCommand{ID: id, Operation: "desktop-metadata", HostSessionID: fixtureHostSession}, desktopIdentity: desktopIdentityFixture(t),
		ctx: context.Background(), changed: make(chan struct{}, 1)}
	pullMetadata(t, a, nonce)
	for name, result := range map[string]modCommandResult{
		"private-reason": {ID: id, Metadata: &desktopMetadata{Status: "invalid", Reason: "private raw native output"}},
		"success-reason": {ID: id, OK: true, Metadata: &desktopMetadata{Status: "matched", SessionID: fixtureSession, HostSessionID: fixtureHostSession, Reason: "text-json"}},
		"missing-host":   {ID: id, OK: true, Metadata: &desktopMetadata{Status: "matched", SessionID: fixtureSession}},
		"wrong-host":     {ID: id, OK: true, Metadata: &desktopMetadata{Status: "matched", SessionID: fixtureSession, HostSessionID: "local_" + fixtureSession}},
		"wrong-command":  {ID: strings.Repeat("w", 43), OK: true, Metadata: &desktopMetadata{Status: "matched", SessionID: fixtureSession}},
		"missing":        {ID: id, OK: true},
		"wrong-session":  {ID: id, OK: true, Metadata: &desktopMetadata{Status: "matched", SessionID: fixtureOther}},
		"missing-id":     {ID: id, OK: true, Metadata: &desktopMetadata{Status: "matched"}},
		"matched-false":  {ID: id, Metadata: &desktopMetadata{Status: "matched", SessionID: fixtureSession}},
		"large-title":    {ID: id, OK: true, Metadata: &desktopMetadata{Status: "matched", SessionID: fixtureSession, HostSessionID: fixtureHostSession, Title: stringPointer(strings.Repeat("字", 1366))}},
		"refusal-field":  {ID: id, OK: true, Refusal: "private", Metadata: &desktopMetadata{Status: "matched", SessionID: fixtureSession}},
		"failed-id":      {ID: id, Metadata: &desktopMetadata{Status: "refused", SessionID: fixtureSession}},
		"failed-title":   {ID: id, Metadata: &desktopMetadata{Status: "timeout", Title: stringPointer("private")}},
		"unknown":        {ID: id, Metadata: &desktopMetadata{Status: "unknown"}},
		"failed-ok":      {ID: id, OK: true, Metadata: &desktopMetadata{Status: "invalid"}},
	} {
		t.Run(name, func(t *testing.T) {
			status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce, Result: &result})
			if status != http.StatusConflict || r.pending.result != nil {
				t.Fatal("invalid readback partially committed")
			}
		})
	}
	valid := modCommandResult{ID: id, OK: true, Metadata: &desktopMetadata{
		Status: "matched", SessionID: fixtureSession, HostSessionID: fixtureHostSession, Title: stringPointer(strings.Repeat("字", 1365) + "a")}}
	for i := 0; i < 2; i++ {
		status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce, Result: &valid})
		if status != http.StatusOK {
			t.Fatal("identical decoded title was compared by pointer")
		}
	}
	valid.Metadata.Title = stringPointer("changed")
	status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce, Result: &valid})
	if status != http.StatusConflict {
		t.Fatal("duplicate command changed readback evidence")
	}
	for _, state := range []string{"refused", "invalid", "timeout", "unavailable", "busy"} {
		r.pending.result = nil
		result := modCommandResult{ID: id, Metadata: &desktopMetadata{Status: state}}
		status, _ := bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce, Result: &result})
		if status != http.StatusOK {
			t.Fatalf("bounded failure status refused: %s", state)
		}
	}
	r.pending.command.Operation = "stop"
	r.pending.result = nil
	status, _ = bridgeCall(t, a, "result", envelope{SessionID: fixtureSession, Nonce: nonce, Result: &valid})
	if status != http.StatusConflict {
		t.Fatal("metadata polluted a different native command")
	}
}
