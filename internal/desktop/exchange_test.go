package desktop

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gatedConnection struct {
	net.Conn
	armed     atomic.Bool
	started   chan struct{}
	release   chan struct{}
	done      chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func (g *gatedConnection) Write(body []byte) (int, error) {
	if g.armed.Load() {
		g.startOnce.Do(func() { close(g.started) })
		select {
		case <-g.release:
		case <-g.done:
			return 0, io.ErrClosedPipe
		}
	}
	return g.Conn.Write(body)
}

func (g *gatedConnection) Close() error {
	g.closeOnce.Do(func() { close(g.done) })
	return g.Conn.Close()
}

func TestBlockedWriterDeadlineDropsExpiredQueuedInput(t *testing.T) {
	client, messages, _ := fixture(t, time.Second, func(peer net.Conn, m Message) { _ = success(peer, m, nil) })
	var gated *gatedConnection
	dial := client.options.Dial
	client.options.Dial = func(ctx context.Context) (io.ReadWriteCloser, error) {
		wire, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		gated = &gatedConnection{Conn: wire.(net.Conn), started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
		return gated, nil
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	gated.armed.Store(true)
	first := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go func() { _, err := client.Request(ctx, "in-flight", 1, nil, "owner"); first <- err }()
	select {
	case <-gated.started:
	case <-time.After(time.Second):
		t.Fatal("Write did not start")
	}
	queued, cancelQueued := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelQueued()
	if _, err := client.Request(queued, "expired", 1, nil, "owner"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := <-first; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(gated.release)
	if _, err := client.Request(context.Background(), "after", 1, nil, "owner"); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"initialize", "in-flight", "after"} {
		if text(nextMessage(t, messages), "method") != method {
			t.Fatal("Expired queued input was emitted")
		}
	}
	if len(messages) != 0 {
		t.Fatal("Extra input")
	}
}

func TestBackpressureAndCloseSettleOnlyOwnedConnection(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	x := newExchange(local, time.Second, nil)
	var closed atomic.Int32
	x.onClose = func() { closed.Add(1) }
	for range 64 {
		if err := x.enqueue(Message{"type": "broadcast"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.enqueue(Message{"type": "broadcast"}, ""); err == nil {
		t.Fatal("Unbounded write admission")
	}
	if closed.Load() != 1 || x.queued != 0 || len(x.writes) != 0 {
		t.Fatal("Failed follower retained writes")
	}
	x.close(io.ErrClosedPipe)
	if closed.Load() != 1 {
		t.Fatal("Repeated close notification")
	}
}

func TestCancellingOneConnectWaiterDoesNotCancelOtherWaiters(t *testing.T) {
	client, _, dials := fixture(t, time.Second, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	client.options.BeforeConnect = func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- client.Connect(ctx) }()
	<-entered
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 1 {
		t.Fatal("Shared connect was replaced")
	}
}
