package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A settings command is independent of Mod event arguments. Qualify whether the
// task-free plugin test host actually invokes it before relying on its stdin.
func TestOfficialModNativeSettingsGuardHost(t *testing.T) {
	engine := nativeModTestEngine(t)
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("settings-hook fixture needs installed Windows PowerShell")
	}
	plugin := t.TempDir()
	for _, relative := range []string{".claude-plugin/plugin.json", "hooks/register.js", "hooks/desktop.mjs"} {
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
	target := filepath.Join(plugin, "settings-guard-target.txt")
	script := filepath.Join(plugin, "deny-read.ps1")
	receipt := filepath.Join(plugin, "native-hook-stdin.json")
	controlReceipt := filepath.Join(plugin, "control-hook-stdin.json")
	for path, data := range map[string]string{
		target: "synthetic settings guard fixture\n",
		script: nativeSettingsGuardCommand,
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// The independent process control checks the script, stdin receipt and deny
	// exit code only. It does not claim that native hooks have run.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	control := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-ReceiptPath", controlReceipt)
	control.SysProcAttr = nativeProcessAttributes()
	controlInput := `{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{}}`
	control.Stdin = strings.NewReader(controlInput)
	controlOutput, controlErr := control.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(controlErr, &exitError) || exitError.ExitCode() != 2 || !strings.Contains(string(controlOutput), "NATIVE_SETTINGS_GUARD_DENIED") {
		t.Fatalf("settings deny script control failed: %v\n%s", controlErr, controlOutput)
	}
	controlData, err := os.ReadFile(controlReceipt)
	if err != nil || string(controlData) != controlInput {
		t.Fatalf("settings script stdin control failed: %v", err)
	}
	command := `"` + filepath.ToSlash(powershell) + `" -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "` + filepath.ToSlash(script) + `" -ReceiptPath "` + filepath.ToSlash(receipt) + `"`
	hooks := map[string]any{
		"description": "Temporary independent native settings-hook denial fixture",
		"modules":     []string{"./register.js"},
		"hooks": map[string]any{"PreToolUse": []any{map[string]any{
			"matcher": "Read",
			"hooks": []any{map[string]any{
				"type": "command", "command": command, "timeout": 5,
			}},
		}}},
	}
	hookJSON, err := json.Marshal(hooks)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, "hooks", "hooks.json"), hookJSON, 0600); err != nil {
		t.Fatal(err)
	}
	targetJSON, err := json.Marshal(filepath.ToSlash(target))
	if err != nil {
		t.Fatal(err)
	}
	fixture := strings.ReplaceAll(nativeSettingsGuardFixture, "__FIXTURE_FILE_JSON__", string(targetJSON))
	if err := os.WriteFile(filepath.Join(plugin, "settings.test.ts"), []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	validation := runNativeModTest(t, engine, "plugin", "validate", "--json", plugin)
	var report struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(validation, &report); err != nil || !report.Success {
		t.Fatalf("native settings-hook plugin validation failed: %v\n%s", err, validation)
	}
	t.Log("independent deny script control and native combined modules/settings plugin validation passed")
	output, runErr := nativeGuardHostRun(engine, plugin)
	t.Logf("%s", output)
	received, receiptErr := os.ReadFile(receipt)
	if errors.Is(receiptErr, os.ErrNotExist) && runErr != nil &&
		strings.Contains(string(output), "HooksError: no implementation for tool.call") &&
		strings.Contains(string(output), "0 pass") && strings.Contains(string(output), "1 fail") {
		t.Skip("native task-free plugin test host has no core tool.call implementation and did not invoke configured settings PreToolUse command; independent source/session provenance remains unqualified")
	}
	if runErr != nil || receiptErr != nil {
		t.Fatalf("native settings guard failed: dispatch=%v receipt=%v", runErr, receiptErr)
	}
	var event struct {
		HookEvent string `json:"hook_event_name"`
		Tool      string `json:"tool_name"`
		ToolUseID string `json:"tool_use_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(received, &event); err != nil {
		t.Fatalf("native settings command input: %v", err)
	}
	if event.HookEvent != "PreToolUse" || event.Tool != "Read" || event.ToolUseID == "" || event.SessionID == "" {
		t.Fatalf("native settings command did not carry qualified event/session/tool identity: %+v", event)
	}
	if !strings.Contains(string(output), "1 pass") || !strings.Contains(string(output), "0 fail") {
		t.Fatal("native settings guard assertion did not execute")
	}
}

const nativeSettingsGuardCommand = `param([Parameter(Mandatory=$true)][string]$ReceiptPath)
$ErrorActionPreference = 'Stop'
$payload = [Console]::In.ReadToEnd()
[IO.File]::WriteAllText($ReceiptPath, $payload, [Text.UTF8Encoding]::new($false))
[Console]::Error.WriteLine('NATIVE_SETTINGS_GUARD_DENIED')
exit 2
`

const nativeSettingsGuardFixture = `
import { expect, test } from 'claude-code/testing'

test('native settings PreToolUse command denies before Read body', async ($) => {
  const result = await $.tool.call({ tool: 'Read', file_path: __FIXTURE_FILE_JSON__ })
  expect(result.isError).toBe(true)
  expect(result.text.includes('NATIVE_SETTINGS_GUARD_DENIED')).toBe(true)
})
`
