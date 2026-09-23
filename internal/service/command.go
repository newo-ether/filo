package service

import (
	"io"
	"os"
)

// The installed Filo binary carries every entry point of one deployment, so no
// entry point needs a second artifact or a Node.js runtime. The names match the
// TypeScript entry scripts they replace.
const (
	// systemServiceArgument runs the desktop IPC gateway of one service account.
	systemServiceArgument = "system-service"
	// standaloneArgument runs the standalone preview gateway.
	standaloneArgument = "standalone"
	// probeHostIdleArgument reports whether one auxiliary native host owns an
	// active task. It replaces the installer-only `node probe-host-idle.mjs`.
	probeHostIdleArgument = "probe-host-idle"
	// commandUsage is the one line printed for a call that names no entry point.
	commandUsage = "Usage: filo <executor-runner|system-service|standalone|probe-host-idle|request> [arguments]"
)

// Main dispatches one Filo entry point and returns its process exit code. An
// unknown or missing entry point is a usage error, which never starts a service.
func Main(arguments []string, stderr io.Writer) int {
	if stderr == nil {
		stderr = os.Stderr
	}
	if len(arguments) == 0 {
		_, _ = io.WriteString(stderr, commandUsage+"\n")
		return 2
	}
	switch arguments[0] {
	case "request":
		return RunRequest(arguments[1:], nil, stderr)
	case executorRunnerArgument:
		return RunTaskExecutorRunner(arguments[1:])
	case systemServiceArgument:
		return RunSystemService(arguments[1:], stderr)
	case standaloneArgument:
		return RunStandalone(arguments[1:], stderr)
	case probeHostIdleArgument:
		return RunHostIdleProbe(arguments[1:], stderr)
	}
	_, _ = io.WriteString(stderr, commandUsage+"\n")
	return 2
}
