package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		if os.Getenv("FILO_TEST_CLAUDE_METADATA") == "invalid" {
			models = "invalid catalog"
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": request.RequestID, "response": map[string]any{"models": models}}})
	}
	_, _ = log.WriteString("eof\n")
	return 0
}

func TestClaudeModelsBootstrapWithoutSessionOrBroker(t *testing.T) {
	for _, mode := range []string{"success", "invalid", "cancelled", "scope-changed", "closed"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			logPath := filepath.Join(root, "native.log")
			t.Setenv(serviceHelperEnvironment, "claude-metadata")
			t.Setenv(serviceHelperLogEnvironment, logPath)
			t.Setenv("FILO_TEST_CLAUDE_METADATA", mode)
			if mode == "cancelled" {
				t.Setenv("FILO_TEST_CLAUDE_METADATA", "blocked")
			}
			launches := 0
			s, err := NewClaudeExecutorSessions(ClaudeExecutorOptions{Directory: filepath.Join(root, "broker"),
				Workspace: root, NativeScopeID: "metadata-fixture", Executable: os.Args[0],
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
