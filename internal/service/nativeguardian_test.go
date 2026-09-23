package service

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runGuardianHelper is the lifetime-monitor fixture. It publishes its own pid and
// rebinds whenever the durable state names a task, which is the observable part
// of the installed monitor's contract.
func runGuardianHelper(arguments []string) int {
	directory := ""
	for index, argument := range arguments {
		if argument == "-StateDirectory" && index+1 < len(arguments) {
			directory = arguments[index+1]
		}
	}
	if directory == "" {
		return 2
	}
	published := false
	task := ""
	for {
		current := ""
		if value, err := ReadExecutorFile(directory, executorStateName); err == nil {
			object, _ := value.(map[string]any)
			if text, ok := object["taskId"].(string); ok {
				current = text
			}
		}
		if !published || current != task {
			record := map[string]any{"pid": os.Getpid()}
			if current != "" {
				record["taskId"] = current
			}
			if err := SaveExecutorFile(directory, guardianReadyName, record); err != nil {
				return 2
			}
			published, task = true, current
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runGuardianWaitHelper never publishes readiness, which is how a monitor that
// cannot protect its task is exercised. It is detached, so it ends itself.
func runGuardianWaitHelper() int {
	time.Sleep(5 * time.Second)
	return 0
}

// guardianLaunch starts the helper with the monitor arguments of the installed
// launcher, so the spawn contract is exercised rather than replaced. Every
// started monitor is recorded, because a detached fixture process that outlives
// the test keeps this test binary's image locked.
func guardianLaunch(role, pidFile string) func(executable, directory string) (*exec.Cmd, error) {
	return func(executable, directory string) (*exec.Cmd, error) {
		command := exec.Command(executable, "-Role", "TaskGuardian", "-StateDirectory", directory)
		command.Env = append(os.Environ(), serviceHelperEnvironment+"="+role)
		command.SysProcAttr = detachedProcessAttributes()
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Start(); err != nil {
			return nil, err
		}
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(command.Process.Pid)+"\n"), 0o600)
		return command, nil
	}
}

// TestNativeGuardianBindsItsTaskAndReplacesAStaleRecord pins the monitor
// handshake: a stale readiness record is never trusted, the monitor names
// itself, and a bound task is published before protection is assumed.
func TestNativeGuardianBindsItsTaskAndReplacesAStaleRecord(t *testing.T) {
	directory := t.TempDir()
	stale, err := randomUUID()
	if err != nil {
		t.Fatalf("randomUUID: %v", err)
	}
	if err := SaveExecutorFile(directory, guardianReadyName,
		map[string]any{"pid": 1, "taskId": stale}); err != nil {
		t.Fatalf("prepare the stale readiness record: %v", err)
	}
	guardian, err := startGuardianFixture(t, directory, "guardian", 5*time.Second)
	if err != nil {
		t.Fatalf("StartNativeGuardian: %v", err)
	}
	defer killProcess(t, guardian.PID)
	if !guardian.Alive() {
		t.Fatal("the published monitor is not alive")
	}
	record := readGuardianRecord(t, directory)
	if record.pid != guardian.PID || record.taskID != nil {
		t.Fatalf("the initial readiness record = %+v", record)
	}
	task, err := randomUUID()
	if err != nil {
		t.Fatalf("randomUUID: %v", err)
	}
	if err := SaveExecutorState(directory, ExecutorState{
		Phase:     "starting",
		KeeperPid: os.Getpid(),
		TaskID:    &task,
	}); err != nil {
		t.Fatalf("publish the accepted task: %v", err)
	}
	if err := guardian.Bind(task); err != nil {
		t.Fatalf("bind the accepted task: %v", err)
	}
	if bound := readGuardianRecord(t, directory); bound.taskID == nil || *bound.taskID != task {
		t.Fatalf("the bound readiness record = %+v", bound)
	}
}

// TestNativeGuardianRefusesAnUnreachableMonitor pins the protection gate: a
// monitor that never publishes itself fails the executor instead of leaving a
// native task unprotected.
func TestNativeGuardianRefusesAnUnreachableMonitor(t *testing.T) {
	directory := t.TempDir()
	guardian, err := startGuardianFixture(t, directory, "guardian-wait", 300*time.Millisecond)
	if err == nil {
		killProcess(t, guardian.PID)
		t.Fatal("a silent monitor was accepted")
	}
	if err.Error() != "Native task lifetime protection is unavailable" {
		t.Fatalf("a silent monitor = %v", err)
	}
	// A monitor that exited before publishing cannot protect anything either.
	exited, err := startGuardianFixture(t, directory, "guardian-exit", 300*time.Millisecond)
	if err == nil {
		killProcess(t, exited.PID)
		t.Fatal("an exited monitor was accepted")
	}
	if err.Error() != "Native task lifetime protection is unavailable" {
		t.Fatalf("an exited monitor = %v", err)
	}
}

// TestNativeGuardianPropagatesALaunchFailure pins the failure path of the
// installed launcher, before any readiness is awaited.
func TestNativeGuardianPropagatesALaunchFailure(t *testing.T) {
	directory := t.TempDir()
	if err := SaveExecutorFile(directory, guardianReadyName,
		map[string]any{"pid": 1}); err != nil {
		t.Fatalf("prepare a stale readiness record: %v", err)
	}
	guardian, err := StartNativeGuardian(NativeGuardianOptions{
		Directory:  directory,
		Executable: filepath.Join(directory, "missing-monitor.exe"),
		Readiness:  200 * time.Millisecond,
		Launch: func(string, string) (*exec.Cmd, error) {
			return nil, errors.New("monitor launch failed")
		},
	})
	if guardian != nil || err == nil || err.Error() != "monitor launch failed" {
		t.Fatalf("a failed monitor launch = %v, %v", guardian, err)
	}
	// The stale record must be gone before any monitor is started, so a launch
	// failure can never be reported as readiness.
	if _, err := os.Stat(filepath.Join(directory, guardianReadyName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stale readiness record survived the launch: %v", err)
	}
}

// startGuardianFixture starts the helper in one role. The started monitor is
// recorded and released during cleanup, so no detached fixture process can hold
// this test binary's image past the test.
func startGuardianFixture(t *testing.T, directory, role string, readiness time.Duration) (*NativeGuardian, error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), "monitor.pid")
	t.Cleanup(func() { releaseFixtureProcesses(t, pidFile) })
	return StartNativeGuardian(NativeGuardianOptions{
		Directory:  directory,
		Executable: executable,
		Readiness:  readiness,
		Launch:     guardianLaunch(role, pidFile),
	})
}

// releaseFixtureProcesses ends every monitor a fixture started and waits until
// each is really gone, because a running image cannot be deleted on Windows.
func releaseFixtureProcesses(t *testing.T, pidFile string) {
	t.Helper()
	content, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(content), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			continue
		}
		terminateProcess(pid)
		waitForProcessExit(t, pid, "the fixture monitor")
	}
}

// readGuardianRecord reads the published readiness record.
func readGuardianRecord(t *testing.T, directory string) guardianReadyRecord {
	t.Helper()
	value, err := ReadExecutorFile(directory, guardianReadyName)
	if err != nil {
		t.Fatalf("read the readiness record: %v", err)
	}
	record, ok := decodeGuardianReady(value)
	if !ok {
		t.Fatalf("the readiness record = %v", value)
	}
	return record
}

// terminateProcess ends one fixture process and releases the handle Go opened
// for it. Releasing matters on Windows: while any handle to a process object
// exists its pid stays reserved, so a later liveness check would keep seeing a
// process that already ended.
func terminateProcess(pid int) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = process.Kill()
	_ = process.Release()
}

// waitForProcessExit waits until a pid is really gone. A terminated process
// object can outlive its termination by a short moment, so an immediate check
// would report a released child as running.
func waitForProcessExit(t *testing.T, pid int, description string) {
	t.Helper()
	for attempt := 0; attempt < 250 && processAlive(pid); attempt++ {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Errorf("%s %d did not end", description, pid)
	}
}

// killProcess ends one fixture process and waits until it is really gone.
func killProcess(t *testing.T, pid int) {
	t.Helper()
	terminateProcess(pid)
	waitForProcessExit(t, pid, "the fixture process")
}
