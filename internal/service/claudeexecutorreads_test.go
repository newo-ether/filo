package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestClaudeExecutorAttachNotifiesActivityOnlyChanges(t *testing.T) {
	var stage atomic.Int32
	config := claudeBrokerConfig{Token: strings.Repeat("a", 64), Workspace: t.TempDir(), Scope: "fixture"}
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := "running"
		if stage.Load() > 0 {
			state = "succeeded"
		}
		page := protocol.ConversationPage{Messages: []protocol.Message{{
			MessageIdentity: protocol.MessageIdentity{ID: "native-tool", TurnID: "turn", Role: "assistant"},
			Activity:        &protocol.Activity{Type: "tool", ToolName: "Read", State: state},
		}}}
		_ = json.NewEncoder(w).Encode(claudeBrokerResponse{Page: &page})
	}))
	defer broker.Close()
	directory := t.TempDir()
	if err := SaveExecutorFile(directory, claudeBrokerConfigName, config); err != nil {
		t.Fatal(err)
	}
	if err := SaveExecutorFile(directory, claudeBrokerStateName, claudeBrokerState{URL: broker.URL, PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	changed := make(chan string, 1)
	s, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{
		Directory: directory, Workspace: config.Workspace, NativeScopeID: config.Scope,
		OnChange: func(id string) { changed <- id },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	detach, err := s.Attach(ctx, "owned")
	if err != nil {
		t.Fatal(err)
	}
	defer detach()
	stage.Store(1)
	select {
	case id := <-changed:
		if id != "owned" {
			t.Fatal("notification identity changed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("activity-only change did not notify the stream owner")
	}
	plain, err := s.Read(ctx, "owned", "", false, nil)
	if err != nil || len(plain.Messages) != 0 {
		t.Fatal("internal activity observation changed text-only reads", err)
	}
}

func TestClaudeExecutorReadActivityAndExcludedTurns(t *testing.T) {
	rows := []protocol.Message{
		{MessageIdentity: protocol.MessageIdentity{ID: "text-first", TurnID: "first", Role: "assistant"}, Text: "first"},
		{MessageIdentity: protocol.MessageIdentity{ID: "tool-first", TurnID: "first", Role: "assistant"}, Activity: &protocol.Activity{Type: "tool", ToolName: "Read"}},
		{MessageIdentity: protocol.MessageIdentity{ID: "text-second", TurnID: "second", Role: "assistant"}, Text: "second"},
		{MessageIdentity: protocol.MessageIdentity{ID: "thought-second", TurnID: "second", Role: "assistant"}, Activity: &protocol.Activity{Type: "thought"}},
	}
	config := claudeBrokerConfig{Token: strings.Repeat("a", 64), Workspace: t.TempDir(), Scope: "fixture"}
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+config.Token {
			t.Error("missing private broker authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request claudeBrokerRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Operation != "read" || request.ID != "owned" {
			t.Error("unexpected broker read")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(claudeBrokerResponse{Page: &protocol.ConversationPage{Messages: rows}})
	}))
	defer broker.Close()
	directory := t.TempDir()
	if err := SaveExecutorFile(directory, claudeBrokerConfigName, config); err != nil {
		t.Fatal(err)
	}
	if err := SaveExecutorFile(directory, claudeBrokerStateName, claudeBrokerState{URL: broker.URL, PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	s, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{Directory: directory, Workspace: config.Workspace, NativeScopeID: config.Scope})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, check := range []struct {
		name     string
		activity bool
		excluded []string
		want     []string
	}{
		{name: "text-only", want: []string{"text-first", "text-second"}},
		{name: "all-activity", activity: true, want: []string{"text-first", "tool-first", "text-second", "thought-second"}},
		{name: "excluded-turn", activity: true, excluded: []string{"first"}, want: []string{"text-second", "thought-second"}},
		{name: "both-filters", excluded: []string{"second"}, want: []string{"text-first"}},
	} {
		t.Run(check.name, func(t *testing.T) {
			page, err := s.Read(context.Background(), "owned", "", check.activity, check.excluded)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Messages) != len(check.want) {
				t.Fatalf("rows=%d want=%d", len(page.Messages), len(check.want))
			}
			for index, id := range check.want {
				if page.Messages[index].ID != id {
					t.Fatalf("row %d=%s want=%s", index, page.Messages[index].ID, id)
				}
			}
		})
	}
}
