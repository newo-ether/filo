package service

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Status runs on every /v1/info, so the native help check must not spawn the
// executable again while the file is unchanged, and must re-verify after an update.
func TestCLIRuntimeHelpProofIsCachedPerFileIdentity(t *testing.T) {
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	failing := false
	previous := runCLIRuntimeHelp
	runCLIRuntimeHelp = func(string) ([]byte, error) {
		calls++
		if failing {
			return nil, errors.New("launch failed")
		}
		return []byte("app-server --listen --ws-auth capability-token --ws-token-file"), nil
	}
	t.Cleanup(func() {
		runCLIRuntimeHelp = previous
		cliHelpProofs.Lock()
		delete(cliHelpProofs.entries, resolved)
		cliHelpProofs.Unlock()
	})
	for i := 0; i < 3; i++ {
		if _, err := verifyCLIRuntime(path); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("unchanged executable was verified %d times", calls)
	}
	// An updated executable is verified again; a transient launch failure is not remembered.
	if err := os.WriteFile(path, []byte("fixture-updated"), 0o755); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	failing = true
	if _, err := verifyCLIRuntime(path); err == nil {
		t.Fatal("failed help check must reject the executable")
	}
	failing = false
	if _, err := verifyCLIRuntime(path); err != nil || calls != 3 {
		t.Fatalf("updated executable must be re-verified after a transient failure: calls=%d err=%v", calls, err)
	}
}
