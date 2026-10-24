package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestClaudeExecutorTimestampCompatibilityPreservesLiveOwner(t *testing.T) {
	for _, version := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			messageTime := "1792713600.123"
			sessionTime := "1792713600.123"
			if version != 0 {
				messageTime = "1792713600123"
				sessionTime = "1792713600"
			}
			config := claudeBrokerConfig{Token: strings.Repeat("a", 64), Workspace: t.TempDir(), Scope: "fixture"}
			calls := 0
			broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer "+config.Token {
					t.Error("missing private broker authentication")
				}
				var request claudeBrokerRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Operation != "read" {
					t.Error("timestamp read dispatched native mutation")
				}
				_, _ = fmt.Fprintf(w, `{"timestampVersion":%d,"session":{"updatedAt":%s},"sessions":[{"updatedAt":%s}],"page":{"messages":[{"id":"known","timestamp":%s},{"id":"unknown","timestamp":null}],"nodes":[{"id":"known","timestamp":%s},{"id":"unknown","timestamp":null}],"inputReceipts":[{"clientId":"client","turnId":"turn","messageId":"native"}]}}`, version, sessionTime, sessionTime, messageTime, messageTime)
			}))
			defer broker.Close()
			directory := t.TempDir()
			if err := SaveExecutorFile(directory, claudeBrokerConfigName, config); err != nil {
				t.Fatal(err)
			}
			if err := SaveExecutorFile(directory, claudeBrokerStateName, claudeBrokerState{URL: broker.URL, PID: os.Getpid()}); err != nil {
				t.Fatal(err)
			}
			s, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{Directory: directory, Workspace: config.Workspace, NativeScopeID: config.Scope,
				Launch: func(string) error { t.Fatal("read replaced the live owner"); return nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			response, err := s.call(context.Background(), claudeBrokerRequest{Operation: "read", ID: "owned"}, false)
			if version == 2 {
				if err == nil {
					t.Fatal("unknown timestamp schema was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || response.Session.UpdatedAt != 1792713600 || response.Sessions[0].UpdatedAt != 1792713600 ||
				response.Page.Messages[0].Timestamp != 1792713600123 || response.Page.Nodes[0].Timestamp != 1792713600123 ||
				!math.IsNaN(float64(response.Page.Messages[1].Timestamp)) || !math.IsNaN(float64(response.Page.Nodes[1].Timestamp)) ||
				response.Page.InputReceipts[0].MessageID != "native" {
				t.Fatal("timestamp conversion changed units, unknown time, input identity or dispatch count")
			}
			encoded, err := json.Marshal(response.Page)
			if err != nil || strings.Count(string(encoded), `"timestamp":null`) != 2 {
				t.Fatal("unknown time was lost while forwarding the broker response", err)
			}
		})
	}
}
