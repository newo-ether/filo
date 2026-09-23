package desktop

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
)

func TestInitializationRejectsInvalidClientIdentity(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	client := NewClient(Options{Timeout: time.Second, Dial: func(context.Context) (io.ReadWriteCloser, error) { return local, nil }})
	defer client.Close()
	go func() {
		value, err := ReadFrame(peer)
		if err != nil {
			return
		}
		message, _ := nativejson.Fields(value)
		_ = success(peer, message, map[string]any{"clientId": 7})
	}()
	if err := client.Connect(context.Background()); err == nil || err.Error() != "Desktop IPC initialization rejected" {
		t.Fatal(err)
	}
	if client.ConnectionStatus().State != "unavailable" {
		t.Fatal("Rejected initialization became connected")
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := ReadFrame(peer); err != io.EOF {
		t.Fatalf("Rejected connection was not disposed: %v", err)
	}
}

func TestCloseDisposesLateDialWithoutInitializing(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	entered, release, opened := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client := NewClient(Options{Dial: func(context.Context) (io.ReadWriteCloser, error) {
		close(entered)
		<-release // Deliberately model a connector that finishes after cancellation.
		close(opened)
		return local, nil
	}})
	defer client.Close()
	result := make(chan error, 1)
	go func() { result <- client.Connect(context.Background()) }()
	<-entered
	client.Close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Closed connect succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked on the connector")
	}
	close(release)
	<-opened
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := ReadFrame(peer); err != io.EOF {
		t.Fatalf("Late connection emitted input or leaked: %v", err)
	}
}

func TestConcurrentQueueDrainNeverBlocksClose(t *testing.T) {
	for range 100 {
		local, peer := net.Pipe()
		x := newExchange(local, time.Second, nil)
		go func() { _, _ = io.Copy(io.Discard, peer); _ = peer.Close() }()
		go x.writeLoop()
		for range 32 {
			if err := x.enqueue(Message{"type": "broadcast"}, ""); err != nil {
				t.Fatal(err)
			}
		}
		closed := make(chan struct{})
		go func() { x.close(io.ErrClosedPipe); close(closed) }()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("Queue drain blocked close")
		}
	}
}
