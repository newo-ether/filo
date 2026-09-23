//go:build windows

package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// windowsPowerShellOutputLimit replaces the TS execFile maxBuffer: one bounded
// diagnostic buffer per stream.
const windowsPowerShellOutputLimit = 65536

// windowsPowerShell runs one absolute script with the Windows-provided shell and
// its own module discovery, never an inherited PowerShell 7 path.
//
// Divergences from the TS promisified execFile: the output bound is enforced
// after the child finished instead of aborting it mid-write, and a timeout kills
// the shell unconditionally rather than sending it a termination signal.
func windowsPowerShell(script string, args []string, timeout time.Duration) (string, error) {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" || !filepath.IsAbs(script) {
		return "", errors.New("An absolute script and Windows environment are required")
	}
	executable := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	ctx := context.Background()
	if timeout > 0 {
		cancel := func() {}
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	command := exec.CommandContext(ctx, executable,
		append([]string{"-NoProfile", "-NonInteractive", "-File", script}, args...)...)
	command.Env = windowsPowerShellEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	stdout := &boundedOutput{limit: windowsPowerShellOutputLimit}
	stderr := &boundedOutput{limit: windowsPowerShellOutputLimit}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		if stdout.overflow || stderr.overflow {
			return "", errors.New("Windows PowerShell output exceeded its buffer")
		}
		return "", err
	}
	if stdout.overflow {
		return "", errors.New("Windows PowerShell output exceeded its buffer")
	}
	return stdout.String(), nil
}

// windowsPowerShellEnvironment mirrors the TS `PSModulePath` deletion: the
// inherited PowerShell 7 module path must never reach the Windows shell.
func windowsPowerShellEnvironment() []string {
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if key, _, found := strings.Cut(entry, "="); found && strings.EqualFold(key, "PSModulePath") {
			continue
		}
		environment = append(environment, entry)
	}
	return environment
}

// boundedOutput collects command output up to a limit and keeps reporting how
// much was dropped instead of buffering without bound.
type boundedOutput struct {
	limit    int
	buffer   bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(chunk []byte) (int, error) {
	room := b.limit - b.buffer.Len()
	if len(chunk) > room {
		if room > 0 {
			_, _ = b.buffer.Write(chunk[:room])
		}
		b.overflow = true
		// The writer keeps accepting bytes: only the caller may decide that an
		// oversized transcript is a failure.
		return len(chunk), nil
	}
	return b.buffer.Write(chunk)
}

func (b *boundedOutput) String() string { return b.buffer.String() }
