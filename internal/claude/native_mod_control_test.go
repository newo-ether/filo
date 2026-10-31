package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

// This opt-in probe exercises the shipped Mod over a real loopback bridge. It
// creates only one tool-free background fixture and stops only its exact native ID.
func TestOfficialModNativeStopBridge(t *testing.T) {
	if os.Getenv("FILO_CLAUDE_MOD_CONTROL_PROBE") != "1" {
		t.Skip("explicit native control probe is disabled")
	}
	engine := nativeModTestEngine(t)
	fixtureCWD := os.Getenv("FILO_CLAUDE_MOD_PROBE_CWD")
	info, err := os.Stat(fixtureCWD)
	if !filepath.IsAbs(fixtureCWD) || err != nil || !info.IsDir() {
		t.Fatal("set FILO_CLAUDE_MOD_PROBE_CWD to an existing trusted isolated probe directory")
	}
	plugin := t.TempDir()
	adapter := fixtureAdapter(t)
	var diagnosticMu sync.Mutex
	requests := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		diagnosticMu.Lock()
		requests[r.URL.Path]++
		diagnosticMu.Unlock()
		adapter.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer func() {
		diagnosticMu.Lock()
		defer diagnosticMu.Unlock()
		t.Logf("fixture bridge request counts=%v", requests)
	}()
	for _, relative := range []string{".claude-plugin/plugin.json", "hooks/hooks.json", "hooks/register.js", "hooks/desktop.mjs"} {
		data, err := os.ReadFile(filepath.Join("mod", filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(plugin, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	tokenFile := filepath.Join(plugin, "token")
	if err := os.WriteFile(tokenFile, []byte(fixtureToken), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{"url": server.URL + bridgePrefix,
		"tokenFile": tokenFile, "sources": []string{"claude-code"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, "filo-config.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	validation := runNativeModTest(t, engine, "plugin", "validate", "--json", plugin)
	var validated struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(validation, &validated); err != nil || !validated.Success {
		t.Fatalf("native control fixture validation failed: %s", validation)
	}
	name, err := randomNonce()
	if err != nil {
		t.Fatal(err)
	}
	name = "filo-mod-stop-" + name[:12]
	defer cleanupNativeModFixture(t, engine, fixtureCWD, name)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, engine, "--bg", "--name", name, "--tools", "",
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "",
		"--plugin-dir", plugin, "--effort", "high", "--system-prompt", "Do not use tools. Follow the user request.",
		"Write 5000 numbered lines, each containing a different sentence. Do not use tools.")
	command.Dir = fixtureCWD
	command.SysProcAttr = nativeProcessAttributes()
	if output, err := command.CombinedOutput(); err != nil {
		diagnostic := strings.NewReplacer(plugin, "[fixture]", name, "[fixture-name]", engine, "[engine]").Replace(string(output))
		if len(diagnostic) > 1024 {
			diagnostic = diagnostic[:1024]
		}
		t.Fatalf("isolated native launch failed: %v: %s", err, diagnostic)
	}
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var id, turn, binding string
	var sawActive, sawModel, sawCatalog bool
	var observedEffort string
	defer func() {
		t.Logf("native snapshot evidence active=%t model=%t catalog=%t effort=%q", sawActive, sawModel, sawCatalog, observedEffort)
	}()
	for id == "" {
		page, err := adapter.List(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Sessions) > 1 {
			t.Fatal("isolated native bridge registered more than one session")
		}
		// The request may resolve a different native effort than the launch flag.
		// Require the observed request value, without treating that flag as applied settings.
		for _, row := range page.Sessions {
			access, err := adapter.Access(ctx, row.ID)
			if err != nil || !access.Capabilities.Stop {
				continue
			}
			view, err := adapter.Read(ctx, row.ID, "", false, nil)
			if err == nil && view.Runtime != nil {
				sawActive = sawActive || view.Runtime.ActiveTurnID != nil
				sawModel = sawModel || view.Runtime.Model != nil
				sawCatalog = sawCatalog || access.Capabilities.Models
				if view.Runtime.Effort.Known && view.Runtime.Effort.Value != nil {
					observedEffort = *view.Runtime.Effort.Value
				}
			}
			if err == nil && view.Runtime != nil && view.Runtime.ActiveTurnID != nil &&
				access.Capabilities.Models && view.Runtime.Model != nil &&
				view.Runtime.Effort.Known && view.Runtime.Effort.Value != nil && validObservedEffort(*view.Runtime.Effort.Value) {
				models, err := adapter.Models(ctx)
				if err != nil || len(models) == 0 {
					continue
				}
				id, turn, binding = row.ID, *view.Runtime.ActiveTurnID, access.Binding
			}
		}
		if id == "" {
			select {
			case <-deadline.C:
				t.Fatal("shipped native Mod did not expose an abort-capable active fixture turn with native model catalog and request effort")
			case <-ticker.C:
			}
		}
	}
	if err := adapter.Stop(protocol.WithAccessBinding(ctx, binding), id, turn); err != nil {
		t.Fatal(err)
	}
	view, err := adapter.Read(ctx, id, "", false, nil)
	if err != nil || view.Runtime == nil || view.Runtime.Status != "idle" ||
		!view.Runtime.CompletedTurnID.Known || view.Runtime.CompletedTurnID.Value == nil ||
		*view.Runtime.CompletedTurnID.Value != turn {
		t.Fatal("native bridge Stop lacked a matching native terminal readback")
	}
	t.Logf("shipped Mod HTTP catalog/effort and Stop confirmed exact native session=%s turn=%s", id, turn)
}

func cleanupNativeModFixture(t *testing.T, engine, fixtureCWD, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, engine, "agents", "--json", "--all", "--cwd", fixtureCWD)
	command.Dir = fixtureCWD
	command.SysProcAttr = nativeProcessAttributes()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Errorf("exact-cwd fixture cleanup discovery failed: %v", err)
		return
	}
	var value any
	if err := json.Unmarshal(output, &value); err != nil {
		t.Errorf("exact-cwd fixture cleanup discovery was not JSON: %v", err)
		return
	}
	rows, ok := value.([]any)
	if object, isObject := value.(map[string]any); isObject {
		rows, ok = object["agents"].([]any)
		if !ok {
			rows, ok = object["sessions"].([]any)
		}
	}
	if !ok {
		t.Error("exact-cwd fixture cleanup had an unknown native schema")
		return
	}
	matched := false
	for _, item := range rows {
		row, ok := item.(map[string]any)
		if !ok || row["name"] != name {
			continue
		}
		matched = true
		id, _ := row["id"].(string)
		sessionID, _ := row["sessionId"].(string)
		if !opaque.MatchString(id) || !nativeUUID.MatchString(sessionID) {
			t.Error("fixture cleanup lacked exact native job and session IDs")
			continue
		}
		stop := exec.CommandContext(ctx, engine, "stop", id)
		stop.Dir = fixtureCWD
		stop.SysProcAttr = nativeProcessAttributes()
		result, err := stop.CombinedOutput()
		if err != nil || !strings.Contains(strings.ToLower(string(result)), "stopped") {
			t.Errorf("exact fixture cleanup was not confirmed for %s: %v", id, err)
		} else {
			t.Logf("exact native fixture cleanup confirmed session=%s", id)
		}
	}
	if !matched {
		t.Log("no exact-name fixture was present in the native exact-cwd catalog")
	}
}
