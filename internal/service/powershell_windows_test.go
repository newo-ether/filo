//go:build windows

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePowerShellScript writes one absolute probe script.
func writePowerShellScript(t *testing.T, body string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "probe.ps1")
	if err := os.WriteFile(script, []byte(body+"\r\n"), 0o600); err != nil {
		t.Fatalf("prepare the probe script: %v", err)
	}
	return script
}

// TestWindowsPowerShellRunsAnAbsoluteScript pins the resolver call: the
// Windows-provided shell runs one absolute script and its standard output is the
// only result.
func TestWindowsPowerShellRunsAnAbsoluteScript(t *testing.T) {
	script := writePowerShellScript(t, `[Console]::Out.Write('{"Executable":"C:\\native\\codex.exe"}')`)
	stdout, err := windowsPowerShell(script, nil, 30*time.Second)
	if err != nil {
		t.Fatalf("windowsPowerShell: %v", err)
	}
	runtime, err := parseDesktopRuntime(stdout)
	if err != nil || runtime != `C:\native\codex.exe` {
		t.Fatalf("the resolved runtime = %q, %v", runtime, err)
	}
}

// TestWindowsPowerShellDropsTheInheritedModulePath pins the inherited-shell
// rule: a PowerShell 7 module path must never reach the Windows shell.
func TestWindowsPowerShellDropsTheInheritedModulePath(t *testing.T) {
	t.Setenv("PSModulePath", "FILO-SENTINEL-MODULE-PATH")
	script := writePowerShellScript(t, `[Console]::Out.Write($env:PSModulePath)`)
	stdout, err := windowsPowerShell(script, nil, 30*time.Second)
	if err != nil {
		t.Fatalf("windowsPowerShell: %v", err)
	}
	if strings.Contains(stdout, "FILO-SENTINEL-MODULE-PATH") {
		t.Fatalf("the inherited module path reached the shell: %q", stdout)
	}
}

// TestWindowsPowerShellRefusesARelativeScript and an oversized transcript pin the
// two gates of the call itself.
func TestWindowsPowerShellRefusesARelativeScriptAndOversizedOutput(t *testing.T) {
	if _, err := windowsPowerShell("probe.ps1", nil, 5*time.Second); err == nil ||
		err.Error() != "An absolute script and Windows environment are required" {
		t.Fatalf("a relative script = %v", err)
	}
	script := writePowerShellScript(t, `[Console]::Out.Write('x' * 70000)`)
	if _, err := windowsPowerShell(script, nil, 30*time.Second); err == nil ||
		err.Error() != "Windows PowerShell output exceeded its buffer" {
		t.Fatalf("an oversized transcript = %v", err)
	}
}
