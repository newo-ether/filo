package service

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/newo-ether/filo/internal/codex"
)

// The host preflight replaced the installer-only Node script
// `scripts/probe-host-idle.mjs`, so what it must be held to is that script's
// contract: one idle record on standard output, one failure line on standard
// error, and a process exit code that decides whether an installer may stop an
// auxiliary native host.
// hostIdleNativeFixture is one auxiliary native host on a real loopback
// listener. It answers exactly the catalog the preflight reads, and it serves no
// Filo gateway, because the preflight talks to the native owner itself.
type hostIdleNativeFixture struct {
	listener net.Listener
	ids      []string
	status   string
	mu       sync.Mutex
	sockets  []*codex.WebSocket
}

// newHostIdleNativeFixture starts one native host reporting the given thread
// catalog and the given status type for every thread.
func newHostIdleNativeFixture(t *testing.T, ids []string, status string) *hostIdleNativeFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the native host: %v", err)
	}
	fixture := &hostIdleNativeFixture{listener: listener, ids: ids, status: status}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		socket, err := codex.UpgradeWebSocket(writer, request)
		if err != nil {
			return
		}
		fixture.mu.Lock()
		fixture.sockets = append(fixture.sockets, socket)
		fixture.mu.Unlock()
		go fixture.serve(socket)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		fixture.mu.Lock()
		sockets := fixture.sockets
		fixture.mu.Unlock()
		for _, socket := range sockets {
			socket.Terminate()
		}
	})
	return fixture
}

// url is the loopback WebSocket endpoint the preflight connects to.
func (f *hostIdleNativeFixture) url() string {
	return "ws://" + f.listener.Addr().String()
}

// serve answers the handshake and the catalog read of one connection.
func (f *hostIdleNativeFixture) serve(socket *codex.WebSocket) {
	for {
		message, err := socket.ReadMessage()
		if err != nil {
			socket.Terminate()
			return
		}
		var packet map[string]any
		if json.Unmarshal(message, &packet) != nil {
			continue
		}
		identifier, hasID := packet["id"]
		if !hasID {
			continue
		}
		result := map[string]any{}
		switch packet["method"] {
		case "thread/loaded/list":
			entries := make([]any, 0, len(f.ids))
			for _, id := range f.ids {
				entries = append(entries, id)
			}
			result = map[string]any{"data": entries, "nextCursor": nil}
		case "thread/read":
			params, _ := packet["params"].(map[string]any)
			threadID, _ := params["threadId"].(string)
			result = map[string]any{"thread": map[string]any{
				"id":     threadID,
				"status": map[string]any{"type": f.status},
			}}
		}
		_ = socket.WriteText(Marshal(map[string]any{"id": identifier, "result": result}))
	}
}

// probeHostIdleOutput runs the preflight entry point against one endpoint and
// reports its exit code with both streams.
func probeHostIdleOutput(t *testing.T, arguments []string) (int, string, string) {
	t.Helper()
	var output, diagnostic strings.Builder
	code := runHostIdleProbe(hostIdleProbeOptions{Arguments: arguments, Output: &output, Error: &diagnostic})
	return code, output.String(), diagnostic.String()
}

// TestHostIdleProbeReportsAnIdleAuxiliaryHost pins the record an installer reads
// before it may stop an auxiliary host: one line, the verified thread count and
// no diagnostic.
func TestHostIdleProbeReportsAnIdleAuxiliaryHost(t *testing.T) {
	fixture := newHostIdleNativeFixture(t, []string{"thread-a", "thread-b"}, "idle")
	code, output, diagnostic := probeHostIdleOutput(t, []string{fixture.url()})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (%s)", code, diagnostic)
	}
	if want := "{\"idle\":true,\"tasks\":2}\n"; output != want {
		t.Fatalf("stdout = %q, want %q", output, want)
	}
	if diagnostic != "" {
		t.Fatalf("stderr = %q, want empty", diagnostic)
	}
}

// TestHostIdleProbeReportsAnEmptyAuxiliaryHost pins the profile of an auxiliary
// host that owns no task at all, which is the ordinary case before an upgrade.
func TestHostIdleProbeReportsAnEmptyAuxiliaryHost(t *testing.T) {
	fixture := newHostIdleNativeFixture(t, nil, "idle")
	code, output, diagnostic := probeHostIdleOutput(t, []string{fixture.url()})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (%s)", code, diagnostic)
	}
	if want := "{\"idle\":true,\"tasks\":0}\n"; output != want {
		t.Fatalf("stdout = %q, want %q", output, want)
	}
}

// TestHostIdleProbeRefusesAnActiveAuxiliaryTask pins the refusal an installer
// needs: an active Filo-owned task must stop an upgrade, and the refusal must
// print no idle record at all.
func TestHostIdleProbeRefusesAnActiveAuxiliaryTask(t *testing.T) {
	fixture := newHostIdleNativeFixture(t, []string{"thread-a"}, "active")
	code, output, diagnostic := probeHostIdleOutput(t, []string{fixture.url()})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if output != "" {
		t.Fatalf("stdout = %q, want empty", output)
	}
	want := hostIdleProbeDiagnosticPrefix + "A Filo-owned auxiliary task is active; finish it before updating\n"
	if diagnostic != want {
		t.Fatalf("stderr = %q, want %q", diagnostic, want)
	}
}

// TestHostIdleProbeRefusesAnEndpointWithoutAHost pins both unreachable cases of
// the preflight: an endpoint nobody listens on and an argument that is missing
// entirely. Neither may report an idle host.
func TestHostIdleProbeRefusesAnEndpointWithoutAHost(t *testing.T) {
	unreachable := "ws://127.0.0.1:" + strconv.Itoa(reserveLoopbackPort(t))
	for _, test := range []struct {
		name      string
		arguments []string
	}{
		{name: "unreachable endpoint", arguments: []string{unreachable}},
		{name: "absent argument", arguments: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, output, diagnostic := probeHostIdleOutput(t, test.arguments)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if output != "" {
				t.Fatalf("stdout = %q, want empty", output)
			}
			if !strings.HasPrefix(diagnostic, hostIdleProbeDiagnosticPrefix) {
				t.Fatalf("stderr = %q, want the %q prefix", diagnostic, hostIdleProbeDiagnosticPrefix)
			}
			if !strings.HasSuffix(diagnostic, "\n") || strings.Count(diagnostic, "\n") != 1 {
				t.Fatalf("stderr = %q, want exactly one line", diagnostic)
			}
		})
	}
}
