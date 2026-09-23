//go:build windows

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const serviceHelperConsoleLogEnvironment = "FILO_TEST_SERVICE_HELPER_CONSOLE_LOG"

func recordServiceHelperConsoleState() {
	path := os.Getenv(serviceHelperConsoleLogEnvironment)
	if path == "" {
		return
	}
	console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	_ = os.WriteFile(path, []byte(fmt.Sprintf("console=%d;pid=%d\n", console, os.Getpid())), 0o600)
}

func TestBackgroundChildLaunchersNeverCreateAConsole(t *testing.T) {
	t.Run("executor runner", func(t *testing.T) {
		log := filepath.Join(t.TempDir(), "console.log")
		t.Setenv(serviceHelperEnvironment, "console-exit")
		t.Setenv(serviceHelperConsoleLogEnvironment, log)
		if err := launchExecutorRunner(t.TempDir()); err != nil {
			t.Fatalf("launchExecutorRunner: %v", err)
		}
		assertNoChildConsole(t, log)
	})

	t.Run("native app server", func(t *testing.T) {
		directory := t.TempDir()
		log := filepath.Join(directory, "console.log")
		tokenPath := filepath.Join(directory, "token")
		if err := os.WriteFile(tokenPath, []byte("fixture-token"), 0o600); err != nil {
			t.Fatalf("write token: %v", err)
		}
		url, err := reserveExecutorURL()
		if err != nil {
			t.Fatalf("reserveExecutorURL: %v", err)
		}
		t.Setenv(serviceHelperEnvironment, "native")
		t.Setenv(serviceHelperConsoleLogEnvironment, log)
		command, err := startNativeAppServer(os.Args[0], directory, url, tokenPath)
		if err != nil {
			t.Fatalf("startNativeAppServer: %v", err)
		}
		defer func() {
			_ = command.Process.Kill()
			_ = command.Wait()
		}()
		assertNoChildConsole(t, log)
	})

	t.Run("guardian", func(t *testing.T) {
		directory := t.TempDir()
		log := filepath.Join(directory, "console.log")
		t.Setenv(serviceHelperEnvironment, "guardian")
		t.Setenv(serviceHelperConsoleLogEnvironment, log)
		command, err := launchGuardianProcess(os.Args[0], directory)
		if err != nil {
			t.Fatalf("launchGuardianProcess: %v", err)
		}
		defer func() {
			_ = command.Process.Kill()
			_ = command.Wait()
		}()
		assertNoChildConsole(t, log)
	})
}

func assertNoChildConsole(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Split(strings.TrimSpace(string(content)), ";")
			if len(fields) != 2 || !strings.HasPrefix(fields[0], "console=") || !strings.HasPrefix(fields[1], "pid=") {
				t.Fatalf("invalid child console record %q", content)
			}
			console, err := strconv.ParseUint(strings.TrimPrefix(fields[0], "console="), 10, 64)
			if err != nil {
				t.Fatalf("parse child console handle: %v", err)
			}
			if console != 0 {
				t.Fatalf("child created console handle %d", console)
			}
			return
		}
		if !os.IsNotExist(err) {
			t.Fatalf("read child console record: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("child did not publish its console state")
}
