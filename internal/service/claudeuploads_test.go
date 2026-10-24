package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestClaudeUploadUpgradePreservesLegacyOwner(t *testing.T) {
	directory := t.TempDir()
	config := claudeBrokerConfig{Token: strings.Repeat("a", 64), Workspace: t.TempDir(), Scope: "fixture"}
	calls := 0
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+config.Token {
			t.Error("legacy owner lost authentication")
		}
		var request claudeBrokerRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		calls++
		response := claudeBrokerResponse{}
		if request.Operation == "read" {
			response.Page = &protocol.ConversationPage{Access: &protocol.SessionAccess{
				Binding: "original-owner", Capabilities: protocol.AdapterCapabilities{IdleSend: true},
			}}
		} else if request.Operation == "stop" {
			if request.Binding != "original-owner" || request.TurnID != "original-turn" {
				t.Error("legacy Stop identity changed")
			}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer old.Close()
	if err := SaveExecutorFile(directory, claudeBrokerConfigName, config); err != nil {
		t.Fatal(err)
	}
	if err := SaveExecutorFile(directory, claudeBrokerStateName, claudeBrokerState{URL: old.URL, PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(directory, claudeBrokerConfigName))
	launches := 0
	s, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{Directory: directory, Workspace: config.Workspace,
		NativeScopeID: config.Scope, UploadDirectory: filepath.Join(t.TempDir(), "uploads"),
		Launch: func(string) error { launches++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Capabilities().Uploads || s.legacy == nil || s.options.Directory == directory {
		t.Fatal("upgrade did not separate new sessions from the existing native owner")
	}
	page, err := s.Read(context.Background(), "owned", "", false, nil)
	if err != nil || page.Access == nil || page.Access.Capabilities.Uploads {
		t.Fatalf("legacy read permissions changed: %+v %v", page.Access, err)
	}
	ctx := protocol.WithAccessBinding(context.Background(), "original-owner")
	if err := s.Stop(ctx, "owned", "original-turn"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(directory, claudeBrokerConfigName))
	if string(before) != string(after) || launches != 0 || calls != 2 {
		t.Fatalf("upgrade touched native ownership: launches=%d calls=%d", launches, calls)
	}
}

func TestClaudeUploadUpgradeRoutesOnlyExplicitlyMissingSessions(t *testing.T) {
	for _, outcome := range []string{"missing", "accepted", "error", "http-error", "invalid"} {
		t.Run(outcome, func(t *testing.T) {
			directory := t.TempDir()
			config := claudeBrokerConfig{Token: strings.Repeat("a", 64), Workspace: t.TempDir(), Scope: "fixture"}
			var oldCalls, newCalls atomic.Int32
			serve := func(old bool) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request claudeBrokerRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					if r.Header.Get("Authorization") != "Bearer "+config.Token || request.Operation != "send" ||
						request.ID != "owned" || request.ClientID != "client" || request.Binding != "original-owner" {
						t.Error("native input identity changed")
					}
					response := claudeBrokerResponse{}
					if old {
						oldCalls.Add(1)
						response.Receipt = &protocol.SendReceipt{ClientID: "client", TurnID: "old-turn"}
					} else {
						newCalls.Add(1)
						switch outcome {
						case "missing":
							response.Missing = true
						case "accepted":
							response.Receipt = &protocol.SendReceipt{ClientID: "client", TurnID: "new-turn"}
						case "error":
							response.Error = "native observation is unknown"
						case "http-error":
							http.Error(w, "unknown", http.StatusBadGateway)
							return
						case "invalid":
							_, _ = w.Write([]byte("{"))
							return
						}
					}
					_ = json.NewEncoder(w).Encode(response)
				}))
			}
			old, current := serve(true), serve(false)
			defer old.Close()
			defer current.Close()
			if err := SaveExecutorFile(directory, claudeBrokerConfigName, config); err != nil {
				t.Fatal(err)
			}
			if err := SaveExecutorFile(directory, claudeBrokerStateName, claudeBrokerState{URL: old.URL, PID: os.Getpid()}); err != nil {
				t.Fatal(err)
			}
			s, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{Directory: directory, Workspace: config.Workspace,
				NativeScopeID: config.Scope, UploadDirectory: filepath.Join(t.TempDir(), "uploads"),
				Launch: func(string) error { t.Error("routing launched a replacement owner"); return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := os.MkdirAll(s.options.Directory, 0o700); err != nil {
				t.Fatal(err)
			}
			config.UploadDirectory = s.options.UploadDirectory
			if err := SaveExecutorFile(s.options.Directory, claudeBrokerConfigName, config); err != nil {
				t.Fatal(err)
			}
			if err := SaveExecutorFile(s.options.Directory, claudeBrokerStateName, claudeBrokerState{URL: current.URL, PID: os.Getpid()}); err != nil {
				t.Fatal(err)
			}
			ctx := protocol.WithAccessBinding(context.Background(), "original-owner")
			receipt, err := s.Send(ctx, "owned", "fixture input", "client")
			expectedOld := int32(0)
			switch outcome {
			case "missing":
				expectedOld = 1
				if err != nil || receipt.TurnID != "old-turn" {
					t.Fatalf("legacy input was not routed: %+v %v", receipt, err)
				}
			case "accepted":
				if err != nil || receipt.TurnID != "new-turn" {
					t.Fatalf("current owner receipt was lost: %+v %v", receipt, err)
				}
			default:
				if err == nil {
					t.Fatal("unknown result reported success")
				}
			}
			if oldCalls.Load() != expectedOld || newCalls.Load() != 1 {
				t.Fatalf("input was replayed or misrouted: old=%d new=%d", oldCalls.Load(), newCalls.Load())
			}
		})
	}
}
