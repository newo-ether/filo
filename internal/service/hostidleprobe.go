package service

import (
	"context"
	"io"

	"github.com/newo-ether/filo/internal/codex"
)

// The installer preflight of one auxiliary native host was a Node script,
// `scripts/probe-host-idle.mjs`, because it needed the transpiled Codex client.
// The binary already owns that client, so the preflight is a fourth entry point
// of the same executable and no deployment needs a Node runtime to inspect a
// host it is about to upgrade.
const (
	// hostIdleProbeDiagnosticPrefix leads the one failure line of this entry
	// point, the shape every entry point of this binary reports failures with.
	hostIdleProbeDiagnosticPrefix = "Filo host probe failed: "
)

// hostIdleProbeOptions configures one host preflight.
type hostIdleProbeOptions struct {
	// Arguments are the entry point arguments. The first one is the private
	// loopback WebSocket endpoint of the auxiliary host. An absent argument is
	// refused by the endpoint validation itself, which is where the TypeScript
	// script's undefined argument was refused.
	Arguments []string
	// Output receives the idle record. Nil selects standard output.
	Output io.Writer
	// Error receives the one failure line. Nil selects standard error.
	Error io.Writer
}

// RunHostIdleProbe runs the auxiliary host preflight and returns the process
// exit code: 0 when every visible native thread is idle, 1 otherwise.
func RunHostIdleProbe(arguments []string, stderr io.Writer) int {
	return runHostIdleProbe(hostIdleProbeOptions{Arguments: arguments, Error: stderr})
}

// hostIdleProbeReport is the one record an installer reads. The field order
// matches the TypeScript `JSON.stringify` key order of the script it replaces.
type hostIdleProbeReport struct {
	Idle  bool `json:"idle"`
	Tasks int  `json:"tasks"`
}

// runHostIdleProbe verifies one host and reports it. The probe owns its
// connection for its whole lifetime, so closing it is the last thing that
// happens and no half-open client survives a refusal.
func runHostIdleProbe(options hostIdleProbeOptions) int {
	host, err := connectHostIdleProbe(options.Arguments)
	if err != nil {
		writeEntryDiagnostic(options.Error, hostIdleProbeDiagnosticPrefix, err.Error())
		return 1
	}
	defer host.Close()
	tasks, err := codex.VerifyHostIdle(host.Rpc)
	if err != nil {
		writeEntryDiagnostic(options.Error, hostIdleProbeDiagnosticPrefix, err.Error())
		return 1
	}
	writeEntryRecord(options.Output, hostIdleProbeReport{Idle: true, Tasks: tasks})
	return 0
}

// connectHostIdleProbe opens the one connection the preflight reads through. The
// credential is absent by design: the auxiliary host is reached over loopback by
// the account that owns it, and the preflight adds no sign-in step.
func connectHostIdleProbe(arguments []string) (*codex.CodexHost, error) {
	endpoint := ""
	if len(arguments) > 0 {
		endpoint = arguments[0]
	}
	return codex.ConnectCodexHost(context.Background(), endpoint, codex.HostOptions{})
}
