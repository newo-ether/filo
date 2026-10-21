//go:build windows || linux

package codex

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type localDaemonFixture struct {
	options    LocalDaemonOptions
	methods    chan string
	handshakes atomic.Int32
}

func startLocalDaemonFixture(t *testing.T, stall bool) *localDaemonFixture {
	t.Helper()
	directory, _, user := daemonFixtureDirectory(t)
	f := &localDaemonFixture{methods: make(chan string, 32)}
	f.options = LocalDaemonOptions{SocketPath: filepath.Join(directory, "control.sock"), UserID: user,
		Timeout: 2 * time.Second, VerifyIdentity: func(context.Context, LocalDaemonIdentity) error { return nil }}
	listener, err := net.Listen("unix", f.options.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var handlers sync.WaitGroup
	sockets := make(map[*WebSocket]bool)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		f.handshakes.Add(1)
		if r.Host != "localhost" || r.URL.Path != "/" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected local daemon upgrade target or credentials")
		}
		socket, err := UpgradeWebSocket(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		sockets[socket] = true
		mu.Unlock()
		defer func() { socket.Terminate(); mu.Lock(); delete(sockets, socket); mu.Unlock() }()
		for {
			packet, ok := readPacketUntilClose(t, socket)
			if !ok {
				return
			}
			name := method(t, packet)
			f.methods <- name
			var result any
			switch name {
			case "initialize":
				if stall {
					continue
				}
				result = map[string]any{"codexHome": directory, "platformFamily": "fixture", "platformOs": "fixture", "userAgent": "fixture"}
			case "initialized":
				continue
			case "model/list":
				result = map[string]any{"data": []any{}, "nextCursor": nil}
			default:
				t.Errorf("unexpected native operation: %s", name)
				return
			}
			if !writePacket(t, socket, map[string]any{"id": packet["id"], "result": result}) {
				return
			}
		}
	})}
	served := make(chan struct{})
	go func() { defer close(served); _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-served
		mu.Lock()
		for socket := range sockets {
			socket.Terminate()
		}
		mu.Unlock()
		handlers.Wait()
	})
	return f
}

func (f *localDaemonFixture) takeMethods(t *testing.T, count int) []string {
	t.Helper()
	names := make([]string, 0, count)
	for range count {
		select {
		case name := <-f.methods:
			names = append(names, name)
		case <-time.After(3 * time.Second):
			t.Fatal("fixture did not receive expected methods", names)
		}
	}
	return names
}

func TestConnectLocalDaemonAuthenticatesUDSAndOwnsOnlyItsConnection(t *testing.T) {
	f := startLocalDaemonFixture(t, false)
	var verified atomic.Int32
	f.options.VerifyIdentity = func(_ context.Context, identity LocalDaemonIdentity) error {
		if identity.UserID != f.options.UserID || identity.CodexHome != filepath.Dir(f.options.SocketPath) || identity.UserAgent != "fixture" {
			return errors.New("identity was not supplied by verified peer")
		}
		verified.Add(1)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	host, err := ConnectLocalDaemon(ctx, f.options)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	cancel()
	if _, err := host.Rpc.RequestTimeout("model/list", map[string]any{}, time.Second); err != nil {
		t.Fatal("successful connection retained its setup cancellation callback: ", err)
	}
	if got := f.takeMethods(t, 3); !reflect.DeepEqual(got, []string{"initialize", "initialized", "model/list"}) {
		t.Fatal(got)
	}
	other, err := ConnectLocalDaemon(context.Background(), f.options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	host.Close()
	host.Close()
	if _, err := other.Rpc.RequestTimeout("model/list", map[string]any{}, time.Second); err != nil {
		t.Fatal("close affected another client: ", err)
	}
	if verified.Load() != 2 || f.handshakes.Load() != 2 {
		t.Fatal("not every connection was verified")
	}
}

func TestConnectLocalDaemonRefusesIdentityBeforeInitializedOrMetadata(t *testing.T) {
	f := startLocalDaemonFixture(t, false)
	refused := errors.New("account/store proof refused")
	f.options.VerifyIdentity = func(context.Context, LocalDaemonIdentity) error { return refused }
	if _, err := ConnectLocalDaemon(context.Background(), f.options); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if got := f.takeMethods(t, 1); !reflect.DeepEqual(got, []string{"initialize"}) {
		t.Fatal(got)
	}
	select {
	case name := <-f.methods:
		t.Fatal("native operation after refusal: ", name)
	default:
	}
}

func TestConnectLocalDaemonRejectsUnsafeAddressBeforeHTTP(t *testing.T) {
	f := startLocalDaemonFixture(t, false)
	options := f.options
	options.UserID = "wrong-selected-user"
	if _, err := ConnectLocalDaemon(context.Background(), options); err == nil {
		t.Fatal("wrong selected user accepted")
	}
	options = f.options
	options.SocketPath = filepath.Join(filepath.Dir(options.SocketPath), strings.Repeat("x", 109))
	if _, err := ConnectLocalDaemon(context.Background(), options); err == nil || !strings.Contains(err.Error(), "AF_UNIX") {
		t.Fatal(err)
	}
	_, unsafe, user := daemonFixtureDirectory(t)
	options = f.options
	options.SocketPath, options.UserID = filepath.Join(unsafe, "control.sock"), user
	if _, err := ConnectLocalDaemon(context.Background(), options); err == nil {
		t.Fatal("non-private socket directory accepted")
	}
	if f.handshakes.Load() != 0 {
		t.Fatal("untrusted socket sent HTTP upgrade")
	}
}

func TestConnectLocalDaemonBoundsStalledInitializeAndCancellation(t *testing.T) {
	f := startLocalDaemonFixture(t, true)
	options := f.options
	options.Timeout = 100 * time.Millisecond
	started := time.Now()
	if _, err := ConnectLocalDaemon(context.Background(), options); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("stalled initialize lost deadline type", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("initialize exceeded overall budget")
	}
	if got := f.takeMethods(t, 1); !reflect.DeepEqual(got, []string{"initialize"}) {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ConnectLocalDaemon(ctx, f.options); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.handshakes.Load() != 1 {
		t.Fatal("cancelled setup contacted daemon")
	}
}
