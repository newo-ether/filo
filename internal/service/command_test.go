package service

import (
	"strings"
	"testing"
)

// The binary is the only artifact of a deployment, so its dispatch table is the
// deployment's own interface: every entry point an installer, a service manager
// or a scheduled task names has to be routable through it, and a call that names
// none of them must never start a service.
// TestMainNamesEveryEntryPointInItsUsage pins the one line an operator reads
// when a launch fails. A verb missing from it is a verb nobody can discover.
func TestMainNamesEveryEntryPointInItsUsage(t *testing.T) {
	for _, argument := range []string{executorRunnerArgument, systemServiceArgument, standaloneArgument, probeHostIdleArgument} {
		if !strings.Contains(commandUsage, argument) {
			t.Fatalf("usage line %q does not name %q", commandUsage, argument)
		}
	}
	if !strings.HasPrefix(commandUsage, "Usage: filo <") {
		t.Fatalf("usage line = %q, want the Usage: filo <...> shape", commandUsage)
	}
}

// TestMainRefusesACallWithoutAnEntryPoint pins that an unusable call is a usage
// error: it prints the usage line and no entry point runs.
func TestMainRefusesACallWithoutAnEntryPoint(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments []string
	}{
		{name: "no argument", arguments: nil},
		{name: "unknown entry point", arguments: []string{"executor"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var diagnostic strings.Builder
			if code := Main(test.arguments, &diagnostic); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if want := commandUsage + "\n"; diagnostic.String() != want {
				t.Fatalf("stderr = %q, want %q", diagnostic.String(), want)
			}
		})
	}
}

// TestMainRoutesTheHostPreflight pins the wiring of the entry point that
// replaced a Node script. An unrouted verb would print the usage line instead,
// so the diagnostic prefix proves the call reached the preflight itself.
func TestMainRoutesTheHostPreflight(t *testing.T) {
	var diagnostic strings.Builder
	if code := Main([]string{probeHostIdleArgument}, &diagnostic); code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr %q)", code, diagnostic.String())
	}
	if !strings.HasPrefix(diagnostic.String(), hostIdleProbeDiagnosticPrefix) {
		t.Fatalf("stderr = %q, want the %q prefix", diagnostic.String(), hostIdleProbeDiagnosticPrefix)
	}
}
