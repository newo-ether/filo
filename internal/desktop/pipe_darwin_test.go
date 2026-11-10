package desktop

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDarwinSocketPeer(t *testing.T) {
	// Unix socket paths have a system length limit.
	directory, err := os.MkdirTemp("/tmp", "filo-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "ipc.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	called := false
	conn, err := DialPipe(ctx, path, func(_ context.Context, pid uint32) error {
		called = true
		if pid != uint32(os.Getpid()) {
			t.Fatalf("peer PID %d", pid)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if !called {
		t.Fatal("Peer verification was not called")
	}
	rejected := errors.New("fixture rejected")
	if conn, err := DialPipe(ctx, path, func(context.Context, uint32) error { return rejected }); conn != nil || !errors.Is(err, rejected) {
		t.Fatalf("%v %v", conn, err)
	}
	for _, mode := range []os.FileMode{0666, 0640} {
		os.Chmod(path, mode)
		if conn, err := DialPipe(ctx, path, func(context.Context, uint32) error { t.Fatal("Unexpected connection attempt"); return nil }); conn != nil || err == nil {
			t.Fatal("Accepted unsafe permissions")
		}
	}
	if _, err := DialPipe(ctx, path, nil); err == nil {
		t.Fatal("Accepted a nil verifier")
	}
}
