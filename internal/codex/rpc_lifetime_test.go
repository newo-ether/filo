package codex

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func awaitRPCExit(t *testing.T, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s did not release its transport goroutine", name)
	}
}

func TestRequestDeadlineIncludesBlockedWrite(t *testing.T) {
	in, input := io.Pipe()
	out, output := io.Pipe()
	t.Cleanup(func() { _ = input.Close(); _ = out.Close() })
	rpc := NewRpc(in, output)
	t.Cleanup(rpc.Close)
	done := make(chan requestResult, 1)
	go func() {
		value, err := rpc.RequestTimeout("fixture", nil, 20*time.Millisecond)
		done <- requestResult{value, err}
	}()
	if result := awaitResult(t, done); result.err == nil || !strings.Contains(result.err.Error(), "timed out") {
		t.Fatalf("blocked write result = %+v", result)
	}
	awaitRPCExit(t, rpc.writerDone, "writer")
	awaitRPCExit(t, rpc.readerDone, "reader")
}

type failingRPCWriter struct {
	mu    sync.Mutex
	calls int
	first chan struct{}
}

func (w *failingRPCWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if w.calls == 1 {
		close(w.first)
		return len(p), nil
	}
	return 0, io.ErrClosedPipe
}

func TestWriteFailureSettlesPendingAndClosesOnce(t *testing.T) {
	in, input := io.Pipe()
	t.Cleanup(func() { _ = input.Close() })
	writer := &failingRPCWriter{first: make(chan struct{})}
	rpc := NewRpc(in, writer)
	t.Cleanup(rpc.Close)
	closed := make(chan error, 4)
	rpc.SetClosedHandler(func(err error) { closed <- err })
	done := make(chan requestResult, 1)
	go func() { value, err := rpc.Request("first", nil); done <- requestResult{value, err} }()
	awaitRPCExit(t, writer.first, "first write")
	if err := rpc.Notify("second", nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write did not fail: %v", err)
	}
	if got := awaitResult(t, done); !errors.Is(got.err, io.ErrClosedPipe) {
		t.Fatalf("pending result = %+v", got)
	}
	awaitRPCExit(t, rpc.writerDone, "writer")
	awaitRPCExit(t, rpc.readerDone, "reader")
	rpc.Close()
	if len(closed) != 1 || !errors.Is(<-closed, io.ErrClosedPipe) {
		t.Fatal("first close cause must be delivered exactly once")
	}
}

func TestCloseInterruptsBlockedWriteAndRejectsFutureWork(t *testing.T) {
	in, input := io.Pipe()
	out, output := io.Pipe()
	t.Cleanup(func() { _ = input.Close(); _ = out.Close() })
	rpc := NewRpc(in, output)
	t.Cleanup(rpc.Close)
	done := make(chan requestResult, 1)
	go func() { value, err := rpc.Request("blocked", nil); done <- requestResult{value, err} }()
	waitFor(t, "blocked writer", func() bool {
		rpc.mu.Lock()
		defer rpc.mu.Unlock()
		for write := range rpc.writes {
			if write.state == rpcWriting {
				return true
			}
		}
		return false
	})
	closed := make(chan struct{})
	go func() { rpc.Close(); close(closed) }()
	awaitRPCExit(t, closed, "Close")
	if result := awaitResult(t, done); result.err == nil {
		t.Fatal("Close returned success for pending request")
	}
	awaitRPCExit(t, rpc.writerDone, "writer")
	awaitRPCExit(t, rpc.readerDone, "reader")
	if err := rpc.Notify("later", nil); err == nil {
		t.Fatal("notification was accepted after Close")
	}
}

func TestQueuedExpiredInputNeverReachesRecoveredTransport(t *testing.T) {
	in, input := io.Pipe()
	out, output := io.Pipe()
	t.Cleanup(func() { _ = input.Close(); _ = out.Close() })
	rpc := NewRpc(in, output)
	t.Cleanup(rpc.Close)
	first := make(chan requestResult, 1)
	go func() { value, err := rpc.Request("first", nil); first <- requestResult{value, err} }()
	waitFor(t, "first write started", func() bool {
		rpc.mu.Lock()
		defer rpc.mu.Unlock()
		return len(rpc.writes) == 1 && len(rpc.queue) == 0
	})
	if _, err := rpc.RequestTimeout("expired-input", nil, 20*time.Millisecond); err == nil {
		t.Fatal("queued request did not expire")
	}
	reader := bufio.NewReader(out)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, `"method":"first"`) {
		t.Fatalf("first line = %q, %v", line, err)
	}
	if _, err := io.WriteString(input, "{\"id\":1,\"result\":true}\n"); err != nil {
		t.Fatal(err)
	}
	if result := awaitResult(t, first); result.err != nil {
		t.Fatal(result.err)
	}
	third := make(chan requestResult, 1)
	go func() { value, err := rpc.Request("third", nil); third <- requestResult{value, err} }()
	line, err = reader.ReadString('\n')
	if err != nil || !strings.Contains(line, `"method":"third"`) {
		t.Fatalf("expired input was written after transport recovered: %q, %v", line, err)
	}
	if _, err := io.WriteString(input, "{\"id\":2,\"result\":\"late\"}\n{\"id\":3,\"result\":true}\n"); err != nil {
		t.Fatal(err)
	}
	if result := awaitResult(t, third); result.err != nil {
		t.Fatal(result.err)
	}
}

func TestUnserializableRequestDoesNotCloseHealthyTransport(t *testing.T) {
	f := newRpcFixture(t)
	if _, err := f.rpc.Request("bad-json", make(chan int)); err == nil {
		t.Fatal("unsupported JSON value accepted")
	}
	next := f.requestAsync(t, "next")
	packet := f.nextLine(t)
	if packet["id"] != float64(2) || packet["method"] != "next" {
		t.Fatalf("failed serialization leaked a message: %+v", packet)
	}
	f.feedLine(t, `{"id":2,"result":true}`)
	if result := awaitResult(t, next); result.err != nil {
		t.Fatal(result.err)
	}
}
