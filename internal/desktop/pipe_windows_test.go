//go:build windows

package desktop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

func pipeFixture(t *testing.T) (string, <-chan net.Conn) {
	t.Helper()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	path := `\\.\pipe\filo-go-test-` + hex.EncodeToString(nonce[:])
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{InputBufferSize: 1024, OutputBufferSize: 1024})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- connection
	}()
	return path, accepted
}

func sameFixtureProcess(_ context.Context, pid uint32) error {
	if pid != uint32(os.Getpid()) {
		return errors.New("Wrong isolated pipe server")
	}
	return nil
}

func TestWindowsPipeVerifiesActualHandleBeforeSending(t *testing.T) {
	path, accepted := pipeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := DialPipe(ctx, path, sameFixtureProcess)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	if server == nil {
		t.Fatal("Missing native pipe server")
	}
	defer server.Close()
	cancel() // A completed dial no longer owns the connection lifetime.
	done := make(chan error, 1)
	go func() { done <- WriteFrame(client, Message{"type": "broadcast"}) }()
	if _, err := ReadFrame(server); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWindowsPipeRejectedIdentityClosesWithoutSending(t *testing.T) {
	path, accepted := pipeFixture(t)
	failure := errors.New("Different original account")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var server net.Conn
	connection, err := DialPipe(ctx, path, func(ctx context.Context, _ uint32) error {
		// Let Accept complete before rejecting. go-winio discards clients that
		// disconnect before Accept observes them, so waiting afterwards can hang.
		select {
		case server = <-accepted:
		case <-ctx.Done():
			return ctx.Err()
		}
		return failure
	})
	if server != nil {
		defer server.Close()
	}
	if connection != nil || !errors.Is(err, failure) {
		t.Fatalf("Rejected peer: %v %v", connection, err)
	}
	if server == nil {
		t.Fatal("Missing isolated pipe server")
	}
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := ReadFrame(server); err != io.EOF {
		t.Fatalf("Rejected peer received data or leaked: %v", err)
	}
}

func TestWindowsPipeCancellationDuringVerificationSendsNothing(t *testing.T) {
	path, accepted := pipeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	var server net.Conn
	go func() {
		connection, err := DialPipe(ctx, path, func(ctx context.Context, _ uint32) error {
			select {
			case server = <-accepted:
			case <-ctx.Done():
				return ctx.Err()
			}
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		})
		if connection != nil {
			_ = connection.Close()
			done <- errors.New("Cancelled verification accepted a connection")
			return
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Verification did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Cancelled verification: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Cancelled verification survived")
	}
	if server == nil {
		t.Fatal("Missing isolated pipe server")
	}
	defer server.Close()
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := ReadFrame(server); err != io.EOF {
		t.Fatalf("Cancelled verification sent data or leaked: %v", err)
	}
}

func TestWindowsPipeCancellationUnblocksPendingReadAndWrite(t *testing.T) {
	path, accepted := pipeFixture(t)
	client, err := DialPipe(context.Background(), path, sameFixtureProcess)
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	defer server.Close()
	defer client.Close()
	read, write := make(chan error, 1), make(chan error, 1)
	go func() { var b [1]byte; _, err := client.Read(b[:]); read <- err }()
	go func() { _, err := client.Write(make([]byte, 1<<20)); write <- err }()
	select {
	case err := <-write:
		t.Fatalf("Fixture write did not block: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	for _, result := range []chan error{read, write} {
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("Closed I/O succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("Named-pipe I/O survived Close")
		}
	}
}

func TestWindowsPipeRequiresLocalEndpointAndIdentityValidator(t *testing.T) {
	for _, path := range []string{`\\server\pipe\codex-ipc`, `\\.\pipe\`, `\\.\pipe\bad\path`, "bad"} {
		if connection, err := DialPipe(context.Background(), path, sameFixtureProcess); connection != nil || err == nil {
			t.Fatal("Invalid pipe accepted", path)
		}
	}
	if connection, err := DialPipe(context.Background(), DefaultPipePath, nil); connection != nil || err == nil {
		t.Fatal("Identity check is optional")
	}
}
