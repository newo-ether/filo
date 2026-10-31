package codex

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRequestContextCancellationPreservesSharedConnection(t *testing.T) {
	f := newRpcFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan requestResult, 1)
	go func() {
		value, err := f.rpc.RequestContext(ctx, "cancel", nil, time.Second)
		done <- requestResult{value, err}
	}()
	f.nextLine(t)
	cancel()
	if result := awaitResult(t, done); !errors.Is(result.err, context.Canceled) {
		t.Fatal(result.err)
	}
	next := f.requestAsync(t, "next")
	f.nextLine(t)
	f.feedLine(t, `{"id":1,"result":"late"}`)
	f.feedLine(t, `{"id":2,"result":"ok"}`)
	if result := awaitResult(t, next); result.err != nil || result.value != "ok" {
		t.Fatal(result)
	}
}

func TestBoundedRpcRejectsOversizedPacketBeforeDecode(t *testing.T) {
	input, output := io.Pipe()
	rpc := NewBoundedRpc(input, io.Discard, 8192)
	defer rpc.Close()
	defer output.Close()
	done := make(chan error, 1)
	go func() { _, err := rpc.Request("read", nil); done <- err }()
	_, _ = output.Write([]byte(strings.Repeat("x", 8193)))
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "exceeds its read bound") {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("oversized packet left request pending")
	}
}
