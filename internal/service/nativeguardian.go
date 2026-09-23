package service

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

const (
	// guardianReadyName is the readiness record the guardian publishes. It names
	// the guardian pid and, once bound, the task it protects.
	guardianReadyName = "guardian.ready"
	// guardianExecutableName is the lifetime monitor shipped with the
	// deployment.
	guardianExecutableName = "FiloBackground.exe"
	// guardianReadiness bounds how long a keeper waits for the monitor to
	// publish itself, and guardianReadinessPoll is the TS `delay(50)`.
	guardianReadiness     = 20 * time.Second
	guardianReadinessPoll = 50 * time.Millisecond
)

// NativeGuardianOptions configures one lifetime monitor.
type NativeGuardianOptions struct {
	// Directory is the private executor directory the monitor watches.
	Directory string
	// Executable is the monitor to start. Empty selects the installed
	// `tools/FiloBackground.exe` of the deployment root.
	Executable string
	// Readiness bounds both the initial handshake and every later binding. Zero
	// selects guardianReadiness.
	Readiness time.Duration
	// Launch starts the monitor and returns its running process. Nil selects the
	// installed `-Role TaskGuardian -StateDirectory <directory>` invocation.
	Launch func(executable, directory string) (*exec.Cmd, error)
}

// NativeGuardian is one lifetime monitor that holds this keeper's native task
// independent of the keeper process tree.
type NativeGuardian struct {
	// PID is the monitor process identity published in guardian.ready.
	PID int
	// Alive reports whether the monitor is still running.
	Alive func() bool
	// Ready waits until the monitor published this pid without a bound task.
	Ready func() error
	// Bind waits until the monitor published the given task identity, which is
	// the proof that protection reached the native child before it was used.
	Bind func(taskID string) error
}

// StartNativeGuardian publishes one lifetime monitor and waits for its initial
// readiness. No process-tree ownership is involved: the monitor survives a lost
// keeper and holds exact process handles of its own.
func StartNativeGuardian(options NativeGuardianOptions) (*NativeGuardian, error) {
	// A stale record must never be mistaken for this monitor's readiness.
	if err := os.Remove(filepath.Join(options.Directory, guardianReadyName)); err != nil &&
		!errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	executable := options.Executable
	if executable == "" {
		var err error
		executable, err = deploymentPath("tools", guardianExecutableName)
		if err != nil {
			return nil, err
		}
	}
	launch := options.Launch
	if launch == nil {
		launch = launchGuardianProcess
	}
	command, err := launch(executable, options.Directory)
	if err != nil {
		return nil, err
	}
	readiness := options.Readiness
	if readiness <= 0 {
		readiness = guardianReadiness
	}
	// Reaping the monitor keeps Alive meaningful; the TS unref only stops the
	// child from holding the parent's event loop open.
	var exited atomic.Bool
	pid := command.Process.Pid
	alive := func() bool {
		if exited.Load() {
			return false
		}
		return processAlive(pid)
	}
	go func() {
		_ = command.Wait()
		exited.Store(true)
	}()
	guardian := &NativeGuardian{PID: pid, Alive: alive}
	guardian.Ready = func() error {
		return waitForGuardianReady(options.Directory, pid, nil, readiness, alive)
	}
	guardian.Bind = func(taskID string) error {
		return waitForGuardianReady(options.Directory, pid, &taskID, readiness, alive)
	}
	if err := guardian.Ready(); err != nil {
		return nil, err
	}
	return guardian, nil
}

// launchGuardianProcess starts the installed monitor detached, with no
// inherited diagnostics.
func launchGuardianProcess(executable, directory string) (*exec.Cmd, error) {
	command := exec.Command(executable, "-Role", "TaskGuardian", "-StateDirectory", directory)
	command.SysProcAttr = detachedProcessAttributes()
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		return nil, err
	}
	return command, nil
}

// waitForGuardianReady polls the readiness record while the monitor is alive. A
// record that never names this monitor is a failure, never a reason to keep a
// native task running unprotected.
func waitForGuardianReady(directory string, pid int, taskID *string, readiness time.Duration,
	alive func() bool) error {
	deadline := time.Now().Add(readiness)
	for time.Now().Before(deadline) && alive() {
		value, err := ReadExecutorFile(directory, guardianReadyName)
		switch {
		case err == nil:
			if record, ok := decodeGuardianReady(value); ok && record.pid == pid &&
				(taskID == nil || (record.taskID != nil && *record.taskID == *taskID)) {
				return nil
			}
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
		time.Sleep(guardianReadinessPoll)
	}
	return errors.New("Native task lifetime protection is unavailable")
}

// guardianReadyRecord is the decoded readiness record: a positive pid and an
// optional task identity, which is absent until the monitor is bound.
type guardianReadyRecord struct {
	pid    int
	taskID *string
}

func decodeGuardianReady(value any) (guardianReadyRecord, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return guardianReadyRecord{}, false
	}
	pid, ok := jsPid(object["pid"])
	if !ok {
		return guardianReadyRecord{}, false
	}
	record := guardianReadyRecord{pid: pid}
	if raw, present := object["taskId"]; present && raw != nil {
		text, isText := raw.(string)
		if !isText {
			return guardianReadyRecord{}, false
		}
		record.taskID = &text
	}
	return record, true
}
