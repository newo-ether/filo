package service

import (
	"io"
	"os"
)

// The installed Filo binary carries every entry point of one deployment, so each
// entry point uses the same artifact and runs without a Node.js runtime.
const (
	// systemServiceArgument runs the desktop IPC gateway of one service account.
	systemServiceArgument = "system-service"
	// standaloneArgument runs the private standalone user helper.
	standaloneArgument = "standalone"
	// probeHostIdleArgument reports whether one auxiliary native host owns an
	// active task before the installer changes its Filo registration.
	probeHostIdleArgument = "probe-host-idle"
	// commandUsage is the one line printed for a call that names no entry point.
	commandUsage = "Usage: filo <executor-runner|claude-executor-runner|system-service|standalone|probe-host-idle|request> [arguments]"
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
	case "codex-shared-owner":
		if len(arguments) != 2 {
			return 2
		}
		if err := RunCLISharedOwner(arguments[1]); err != nil {
			return 1
		}
		return 0
	case "request":
		return RunRequest(arguments[1:], nil, stderr)
	case ClaudeExecutorRunnerArgument:
		if err := RunClaudeExecutorRunner(arguments[1:]); err != nil {
			_, _ = io.WriteString(stderr, err.Error()+"\n")
			return 1
		}
		return 0
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
