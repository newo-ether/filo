package claude

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The actual native compiler enforces API rules that JS fixture runners cannot.
// These opt-in gates use the selected client's trusted engine, without a session.
func TestOfficialModNativeValidation(t *testing.T) {
	engine := nativeModTestEngine(t)
	plugin, err := filepath.Abs("mod")
	if err != nil {
		t.Fatal(err)
	}
	output := runNativeModTest(t, engine, "plugin", "validate", "--json", plugin)
	var report struct {
		Success  bool `json:"success"`
		Contents []struct {
			Type        string            `json:"type"`
			Errors      []json.RawMessage `json:"errors"`
			Notes       []string          `json:"notes"`
			GatingHooks []struct {
				Hook     string `json:"hook"`
				HasCatch bool   `json:"hasCatch"`
			} `json:"gatingHooks"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("native validation report: %v\n%s", err, output)
	}
	if !report.Success {
		t.Fatalf("native validator rejected shipped Mod:\n%s", output)
	}
	for _, content := range report.Contents {
		if content.Type != "hooks" || len(content.Errors) != 0 || len(content.Notes) == 0 {
			continue
		}
		for _, hook := range content.GatingHooks {
			if hook.Hook == "session.append{door=prompt|response}" && hook.HasCatch {
				return
			}
		}
	}
	t.Fatalf("native validator did not prove a safe append fallback:\n%s", output)
}

func TestOfficialModNativeBehavior(t *testing.T) {
	engine := nativeModTestEngine(t)
	plugin := t.TempDir()
	for _, relative := range []string{".claude-plugin/plugin.json", "hooks/hooks.json", "hooks/register.js", "observer.test.js"} {
		data, err := os.ReadFile(filepath.Join("mod", filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		// The native kit discovers only .test.ts names. The maintained fixture is
		// plain JavaScript; only this disposable native-test input uses its suffix.
		if relative == "observer.test.js" {
			relative = "observer.test.ts"
		}
		path := filepath.Join(plugin, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	output := runNativeModTest(t, engine, "plugin", "test", plugin)
	if !strings.Contains(string(output), "5 pass") || !strings.Contains(string(output), "0 fail") {
		t.Fatalf("native behavior tests did not all execute:\n%s", output)
	}
	t.Logf("%s", output)
}

func nativeModTestEngine(t *testing.T) string {
	t.Helper()
	engine := os.Getenv("FILO_CLAUDE_MOD_VALIDATOR")
	if engine == "" {
		t.Skip("set FILO_CLAUDE_MOD_VALIDATOR to an installed native Claude engine")
	}
	return engine
}

func runNativeModTest(t *testing.T, engine string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, engine, args...)
	command.SysProcAttr = nativeProcessAttributes()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native Mod check failed: %v\n%s", err, output)
	}
	return output
}
