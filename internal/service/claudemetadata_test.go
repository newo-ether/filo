package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/claude"
	"github.com/newo-ether/filo/internal/protocol"
)

// This fixture uses the real native spawn, initialization and pipe lifetime.
// Every received native operation is recorded, including any forbidden input.
func runClaudeMetadataHelper() int {
	log, err := os.OpenFile(os.Getenv(serviceHelperLogEnvironment), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 2
	}
	defer log.Close()
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return 3
		}
		_, _ = log.WriteString(request.Type + ":" + request.Request.Subtype + "\n")
		if request.Type != "control_request" || request.Request.Subtype != "initialize" {
			return 4
		}
		if os.Getenv("FILO_TEST_CLAUDE_METADATA") == "blocked" {
			continue
		}
		var models any = []any{map[string]any{"value": "native-model", "displayName": "Native model",
			"supportsEffort": true, "supportedEffortLevels": []string{"low", "high"}}}
		if os.Getenv("FILO_TEST_CLAUDE_METADATA") == "duplicates" {
			models = []any{
				map[string]any{"value": "default", "displayName": "Default (recommended)"},
				map[string]any{"value": "opus", "displayName": "deepseek-v4-pro[1m]", "supportsEffort": true, "supportedEffortLevels": []string{"high"}},
				map[string]any{"value": "fable", "displayName": "deepseek-v4-pro[1m]"},
				map[string]any{"value": "opus", "displayName": "duplicate must not replace first"},
				map[string]any{"value": "sonnet", "displayName": "deepseek-v4-flash[1m]"},
				map[string]any{"value": "haiku", "displayName": "deepseek-v4-flash[1m]"},
			}
		}
		if os.Getenv("FILO_TEST_CLAUDE_METADATA") == "invalid" {
			models = "invalid catalog"
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": request.RequestID, "response": map[string]any{"models": models}}})
	}
	_, _ = log.WriteString("eof\n")
	return 0
}

func seedClaudeMetadataMod(t *testing.T, mod *claude.Adapter) {
	t.Helper()
	call := func(action string, body map[string]any) map[string]any {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "/claude-mod/v1/"+action, strings.NewReader(string(data)))
		request.RemoteAddr = "127.0.0.1:54321"
		request.Header.Set("Authorization", "Bearer "+testToken)
		response := httptest.NewRecorder()
		mod.ServeHTTP(response, request)
		var result map[string]any
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil {
			t.Fatalf("Mod metadata fixture %s status=%d", action, response.Code)
		}
		return result
	}
	const session = "11111111-1111-4111-8111-111111111111"
	registered := call("register", map[string]any{"sessionId": session, "instance": "metadata", "origin": "terminal"})
	call("push", map[string]any{"sessionId": session, "nonce": registered["nonce"], "sequence": 1,
		"events": []any{map[string]any{"kind": "snapshot", "modelOptions": []string{"best", "sonnet[1m]"}, "canSettings": true}}})
}

func TestClaudeModelsBootstrapWithoutSessionOrBroker(t *testing.T) {
	for _, mode := range []string{"success", "duplicates", "invalid", "cancelled", "scope-changed", "closed"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			logPath := filepath.Join(root, "native.log")
			t.Setenv(serviceHelperEnvironment, "claude-metadata")
			t.Setenv(serviceHelperLogEnvironment, logPath)
			t.Setenv("FILO_TEST_CLAUDE_METADATA", mode)
			if mode == "cancelled" {
				t.Setenv("FILO_TEST_CLAUDE_METADATA", "blocked")
			}
			mod, err := claude.New(claude.Config{Token: testToken, NativeScopeID: "metadata-fixture"})
			if err != nil {
				t.Fatal(err)
			}
			seedClaudeMetadataMod(t, mod)
			launches := 0
			s, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{Directory: filepath.Join(root, "broker"),
				Workspace: root, NativeScopeID: "metadata-fixture", Executable: os.Args[0], ReadOnly: mod,
				Launch: func(string) error { launches++; return nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			checks := 0
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ctx = protocol.WithScopeValidation(ctx, func(context.Context) error {
				checks++
				if checks == 2 {
					if mode == "scope-changed" {
						return errors.New("selected account changed")
					}
					if mode == "closed" {
						s.Close()
					}
				}
				return nil
			})
			models, err := s.Models(ctx)
			if mode == "success" {
				if err != nil || len(models) != 1 || models[0].ID != "native-model" || len(models[0].ReasoningEfforts) != 2 || checks != 2 {
					t.Fatalf("native catalog missing: models=%v checks=%d err=%v", models, checks, err)
				}
			} else if mode == "duplicates" {
				want := []string{"default", "opus", "fable", "sonnet", "haiku"}
				if err != nil || len(models) != len(want) || checks != 2 {
					t.Fatalf("native deduplication failed: models=%+v err=%v", models, err)
				}
				for i, id := range want {
					if models[i].ID != id {
						t.Fatalf("native choice identity changed: %+v", models)
					}
				}
				if models[0].Name != "Default (recommended)" || models[1].Name != "deepseek-v4-pro[1m]" ||
					len(models[1].ReasoningEfforts) != 1 || models[2].Name != models[1].Name || models[3].Name != models[4].Name {
					t.Fatalf("native labels/effort or distinct same-label choices changed: %+v", models)
				}
			} else if err == nil || len(models) != 0 {
				t.Fatalf("failed metadata read published models: mode=%s models=%v err=%v", mode, models, err)
			}
			if launches != 0 || s.Status().State == "connected" {
				t.Fatal("metadata read created a broker or established Online")
			}
			if _, err := os.Stat(s.options.Directory); !os.IsNotExist(err) {
				t.Fatal("metadata read wrote execution state")
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				data, _ := os.ReadFile(logPath)
				if strings.Contains(string(data), "eof\n") {
					if string(data) != "control_request:initialize\neof\n" {
						t.Fatalf("unexpected native operations: %q", data)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("task-free child did not close its input: %q", data)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestClaudeModelsRefuseStaleScopeBeforeNativeLaunch(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "native.log")
	t.Setenv(serviceHelperEnvironment, "claude-metadata")
	t.Setenv(serviceHelperLogEnvironment, logPath)
	s, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{Directory: filepath.Join(root, "broker"),
		Workspace: root, NativeScopeID: "metadata-fixture", Executable: os.Args[0]})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := protocol.WithScopeValidation(context.Background(), func(context.Context) error { return errors.New("stale scope") })
	if _, err := s.Models(ctx); err == nil {
		t.Fatal("stale scope accepted")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("stale read launched native process")
	}
}
