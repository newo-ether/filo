package service

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/codex"
)

// The standalone entry point is the preview gateway an operator starts by hand.
// Its tests run the entry function in this process and give it an in-process
// native app server, because the gateway owns no artifact of its own: what it
// must be held to is its directory, its lease and its one native client.

// standaloneNativeFixture is one native app server on a real loopback WebSocket
// listener. It answers the standalone handshake, and in its held role it
// declares the initialize request without answering it, which is how a
// cancellation that arrives during startup is observed.
type standaloneNativeFixture struct {
	listener       net.Listener
	holdInitialize bool
	declared       chan struct{}
	once           sync.Once
	released       sync.Once

	mu          sync.Mutex
	connections int
	sockets     []*codex.WebSocket
	release     func()
}

// newStandaloneNativeFixture starts the native fixture of one test.
func newStandaloneNativeFixture(t *testing.T, holdInitialize bool) *standaloneNativeFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the native fixture: %v", err)
	}
	fixture := &standaloneNativeFixture{
		listener:       listener,
		holdInitialize: holdInitialize,
		declared:       make(chan struct{}),
	}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		socket, err := codex.UpgradeWebSocket(writer, request)
		if err != nil {
			return
		}
		fixture.mu.Lock()
		fixture.connections++
		fixture.sockets = append(fixture.sockets, socket)
		fixture.mu.Unlock()
		go fixture.serve(socket)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		// A handshake a failed test left held is released first, so no gateway
		// goroutine outlives this fixture.
		fixture.answerInitialize()
		_ = server.Close()
		fixture.terminate()
	})
	return fixture
}

// url is the loopback WebSocket endpoint one gateway connects to.
func (f *standaloneNativeFixture) url() string {
	return "ws://" + f.listener.Addr().String()
}

// serve answers the methods of the standalone handshake on one socket.
func (f *standaloneNativeFixture) serve(socket *codex.WebSocket) {
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
		method, _ := packet["method"].(string)
		switch method {
		case "initialize":
			answer := func() { _ = socket.WriteText(standaloneNativeResult(identifier, map[string]any{})) }
			if f.holdInitialize {
				f.mu.Lock()
				f.release = answer
				f.mu.Unlock()
				f.once.Do(func() { close(f.declared) })
				continue
			}
			answer()
		case "thread/loaded/list":
			_ = socket.WriteText(standaloneNativeResult(identifier, map[string]any{
				"data":       []any{},
				"nextCursor": nil,
			}))
		default:
			_ = socket.WriteText(standaloneNativeResult(identifier, map[string]any{}))
		}
	}
}

// standaloneNativeResult is one native response to one request.
func standaloneNativeResult(identifier any, result map[string]any) []byte {
	return Marshal(map[string]any{"id": identifier, "result": result})
}

// awaitInitialize blocks until the held handshake was declared.
func (f *standaloneNativeFixture) awaitInitialize(t *testing.T) {
	t.Helper()
	select {
	case <-f.declared:
	case <-time.After(30 * time.Second):
		t.Fatalf("the native fixture received no initialize request")
	}
}

// answerInitialize releases the held handshake exactly once.
func (f *standaloneNativeFixture) answerInitialize() {
	f.released.Do(func() {
		f.mu.Lock()
		release := f.release
		f.mu.Unlock()
		if release != nil {
			release()
		}
	})
}

// connectionCount is the number of native clients this fixture accepted.
func (f *standaloneNativeFixture) connectionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connections
}

// reachable reports whether the native listener still accepts a connection. The
// probe is refused at the WebSocket upgrade, so it never joins the client count.
func (f *standaloneNativeFixture) reachable() bool {
	connection, err := net.DialTimeout("tcp", f.listener.Addr().String(), time.Second)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

// terminate ends every accepted socket.
func (f *standaloneNativeFixture) terminate() {
	f.mu.Lock()
	sockets := append([]*codex.WebSocket(nil), f.sockets...)
	f.mu.Unlock()
	for _, socket := range sockets {
		socket.Terminate()
	}
}

// awaitEntryInfo polls /v1/info until one record satisfies the caller.
func awaitEntryInfo(t *testing.T, base string, headers map[string]string, accept func(map[string]any) bool) map[string]any {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		if info, ok := readEntryInfo(client, base, headers); ok {
			last = info
			if accept(info) {
				return info
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the gateway never reported the expected info record: %v", last)
	return nil
}

// TestStandaloneHelperValidatesItsCredentialAndStopsOnlyItsGateway pins the
// whole lifecycle of the preview gateway: a credential that is not 256 bits of
// hexadecimal refuses the start before any native client is opened, a prepared
// directory publishes one ready record and one authenticated gateway, and a
// requested shutdown releases only the gateway, never the independent host.
func TestStandaloneHelperValidatesItsCredentialAndStopsOnlyItsGateway(t *testing.T) {
	native := newStandaloneNativeFixture(t, false)
	directory := t.TempDir()
	port := reserveLoopbackPort(t)
	config := Marshal(map[string]any{
		"appServerUrl": native.url(),
		"bind":         "127.0.0.1",
		"port":         port,
	})
	writeEntryFixtureFile(t, filepath.Join(directory, entryConfigName), string(config))
	writeEntryFixtureFile(t, filepath.Join(directory, entryTokenName), "invalid")
	writeEntryFixtureFile(t, filepath.Join(directory, "sessions.json"), "broken")

	var diagnostic strings.Builder
	if code := runStandalone(standaloneOptions{Arguments: []string{directory}, Output: io.Discard, Error: &diagnostic}); code != 1 {
		t.Fatalf("an invalid credential exited with %d", code)
	}
	if !strings.HasPrefix(diagnostic.String(), standaloneDiagnosticPrefix) {
		t.Fatalf("diagnostic = %q", diagnostic.String())
	}
	if count := native.connectionCount(); count != 0 {
		t.Fatalf("a refused credential opened %d native connections", count)
	}
	// The withdrawn session registry is never read, rebuilt or rewritten.
	assertEntryFixtureFile(t, filepath.Join(directory, "sessions.json"), "broken")

	token := entryTestToken(t)
	writeEntryFixtureFile(t, filepath.Join(directory, entryTokenName), token)
	var output strings.Builder
	done := make(chan int, 1)
	go func() {
		done <- runStandalone(standaloneOptions{Arguments: []string{directory}, Output: &output, Error: io.Discard})
	}()
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	headers := entryBearerHeaders(token)
	info := awaitEntryInfo(t, base, headers, func(info map[string]any) bool {
		return info["sessionMode"] == "standalone"
	})
	device, _ := info["device"].(string)
	if !strings.HasSuffix(device, standaloneIdentitySuffix) {
		t.Fatalf("device = %q", device)
	}
	if response := get(t, base+"/v1/info", nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}

	stopped := post(t, base+"/v1/shutdown", "", headers)
	if stopped.StatusCode != http.StatusOK {
		t.Fatalf("shutdown status = %d", stopped.StatusCode)
	}
	stopped.Body.Close()
	if code := awaitEntryExit(t, done); code != 0 {
		t.Fatalf("a requested shutdown exited with %d", code)
	}
	if !native.reachable() {
		t.Fatalf("closing the gateway stopped the independently owned host")
	}
	if count := native.connectionCount(); count != 1 {
		t.Fatalf("the gateway opened %d native connections", count)
	}
	ready := `{"ready":true,"mode":"standalone","address":"127.0.0.1","port":` + strconv.Itoa(port) + "}\n"
	if output.String() != ready {
		t.Fatalf("ready record = %q", output.String())
	}
	assertEntryFixtureFile(t, filepath.Join(directory, entryConfigName), string(config))
	assertEntryFixtureFile(t, filepath.Join(directory, entryTokenName), token)
	assertEntryFixtureFile(t, filepath.Join(directory, "sessions.json"), "broken")
	if _, err := os.Stat(filepath.Join(directory, standaloneLeaseName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the lease of a stopped gateway = %v", err)
	}
}

// TestStandaloneGatewayWithdrawsBeforeAdmissionWhenStartupIsCancelled pins the
// private cancellation request: it is observed while the native handshake is
// still open, it releases the lease without waiting for that handshake, and the
// operator's own cancellation is never reported as a failure.
func TestStandaloneGatewayWithdrawsBeforeAdmissionWhenStartupIsCancelled(t *testing.T) {
	native := newStandaloneNativeFixture(t, true)
	directory := t.TempDir()
	port := reserveLoopbackPort(t)
	config := Marshal(map[string]any{
		"appServerUrl": native.url(),
		"bind":         "127.0.0.1",
		"port":         port,
	})
	writeEntryFixtureFile(t, filepath.Join(directory, entryConfigName), string(config))
	writeEntryFixtureFile(t, filepath.Join(directory, entryTokenName), entryTestToken(t))

	done := make(chan int, 1)
	go func() {
		done <- runStandalone(standaloneOptions{Arguments: []string{directory}, Output: io.Discard, Error: io.Discard})
	}()
	native.awaitInitialize(t)
	writeEntryFixtureFile(t, filepath.Join(directory, standaloneCancellationName), "qualification cancellation")
	lease := filepath.Join(directory, standaloneLeaseName)
	waitForCondition(t, 15*time.Second, "the released lease", func() bool {
		_, err := os.Stat(lease)
		return errors.Is(err, os.ErrNotExist)
	})
	native.answerInitialize()
	if code := awaitEntryExit(t, done); code != 0 {
		t.Fatalf("a cancelled startup exited with %d", code)
	}
	if !native.reachable() {
		t.Fatalf("cancellation stopped the independently owned host")
	}
	connection, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err == nil {
		_ = connection.Close()
		t.Fatalf("a cancelled gateway admitted HTTP")
	}
}

func TestPrivateUserHelperRefusesNetworkPublication(t *testing.T) {
	directory := t.TempDir()
	config := Marshal(map[string]any{"appServerUrl": "ws://127.0.0.1:9999", "bind": "100.100.10.1", "port": 9998})
	writeEntryFixtureFile(t, filepath.Join(directory, entryConfigName), string(config))
	writeEntryFixtureFile(t, filepath.Join(directory, entryTokenName), entryTestToken(t))
	var diagnostic strings.Builder
	code := runStandalone(standaloneOptions{Arguments: []string{directory}, Output: io.Discard, Error: &diagnostic})
	if code != 1 || !strings.Contains(diagnostic.String(), standaloneBindRefusal) {
		t.Fatalf("private helper publication: code=%d diagnostic=%q", code, diagnostic.String())
	}
	if _, err := os.Stat(filepath.Join(directory, standaloneLeaseName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected publication claimed a native helper lease: %v", err)
	}
}
