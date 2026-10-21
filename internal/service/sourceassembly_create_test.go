package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestAssemblyUserWorkerCreateForwardsExpectedNativeScope(t *testing.T) {
	for _, test := range []struct {
		name  string
		scope string
		path  string
	}{
		{"legacy", "", "/v1/sessions"},
		{"scoped", "scope-a", "/v1/adapters/codex-desktop/sessions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newUserWorkerTestServer(t, func(w http.ResponseWriter, _ *http.Request, record userWorkerTestRecord) {
				var input map[string]any
				if err := json.Unmarshal([]byte(record.body), &input); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if record.path != test.path || input["text"] != "fixture" || input["clientId"] != "client-fixture" {
					t.Errorf("create request = %+v", record)
				}
				value, supplied := input["nativeScopeId"]
				if test.scope == "" && supplied || test.scope != "" && value != test.scope {
					t.Errorf("native scope = %v, supplied=%v; want %q", value, supplied, test.scope)
				}
				writeUserWorkerJSON(t, w, http.StatusCreated, map[string]any{
					"id": taskSessionsTaskID, "turnId": taskSessionsTurnID, "clientId": "client-fixture",
				})
			})
			worker := newUserWorkerFixture(t, server.url(), StaticUserWorkerToken(testToken),
				userWorkerUnusedHistory{}, DefaultUserWorkerTimeouts)
			validated := 0
			ctx := protocol.WithNativeScopeID(context.Background(), test.scope)
			ctx = protocol.WithScopeValidation(ctx, func(context.Context) error {
				validated++
				return nil
			})
			session, receipt, err := worker.Create(ctx, "fixture", "client-fixture", protocol.SessionSettings{})
			if err != nil || session.ID != taskSessionsTaskID || receipt.TurnID != taskSessionsTurnID {
				t.Fatalf("create = %+v, %+v, %v", session, receipt, err)
			}
			if validated != 1 || server.callCount() != 1 {
				t.Fatalf("scope validations=%d, HTTP calls=%d", validated, server.callCount())
			}
		})
	}
}

func TestAssemblyUserWorkerCreateRefusesChangedScopeBeforeHTTP(t *testing.T) {
	server := newUserWorkerTestServer(t, func(w http.ResponseWriter, _ *http.Request, _ userWorkerTestRecord) {
		t.Error("changed native scope reached private helper")
		w.WriteHeader(http.StatusConflict)
	})
	worker := newUserWorkerFixture(t, server.url(), StaticUserWorkerToken(testToken),
		userWorkerUnusedHistory{}, DefaultUserWorkerTimeouts)
	refusal := errors.New("native account changed")
	ctx := protocol.WithNativeScopeID(context.Background(), "scope-a")
	ctx = protocol.WithScopeValidation(ctx, func(context.Context) error { return refusal })
	_, _, err := worker.Create(ctx, "fixture", "client-fixture", protocol.SessionSettings{})
	if !errors.Is(err, refusal) || server.callCount() != 0 {
		t.Fatalf("create error=%v, HTTP calls=%d", err, server.callCount())
	}
}
