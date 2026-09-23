package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
)

type rpcFixture struct {
	input *io.PipeWriter
	lines chan string
	rpc   *Rpc
}

func newRpcFixture(t *testing.T) *rpcFixture {
	t.Helper()
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	rpc := NewRpc(inReader, outWriter)
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(outReader)
		scanner.Buffer(make([]byte, 1<<20), 1<<24)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	t.Cleanup(func() {
		rpc.Close()
		_ = inWriter.Close()
		_ = outReader.Close()
	})
	return &rpcFixture{input: inWriter, lines: lines, rpc: rpc}
}

func (f *rpcFixture) feedLine(t *testing.T, text string) {
	t.Helper()
	if _, err := f.input.Write([]byte(text + "\n")); err != nil {
		t.Fatalf("feed: %v", err)
	}
}

func (f *rpcFixture) feedBytes(t *testing.T, text string) {
	t.Helper()
	for i := 0; i < len(text); i++ {
		if _, err := f.input.Write([]byte(text[i : i+1])); err != nil {
			t.Fatalf("feed byte: %v", err)
		}
	}
}

func (f *rpcFixture) nextLine(t *testing.T) map[string]any {
	t.Helper()
	select {
	case line, ok := <-f.lines:
		if !ok {
			t.Fatal("output closed")
		}
		var packet map[string]any
		if err := json.Unmarshal([]byte(line), &packet); err != nil {
			t.Fatalf("output line %q: %v", line, err)
		}
		return packet
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for output line")
		return nil
	}
}

type requestResult struct {
	value any
	err   error
}

func (f *rpcFixture) requestAsync(t *testing.T, method string) <-chan requestResult {
	t.Helper()
	done := make(chan requestResult, 1)
	go func() {
		value, err := f.rpc.Request(method, map[string]any{})
		done <- requestResult{value: value, err: err}
	}()
	return done
}

func awaitResult(t *testing.T, done <-chan requestResult) requestResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for request result")
		return requestResult{}
	}
}

func waitFor(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestCorrelatedResponsesAndByteFragmentedUtf8(t *testing.T) {
	f := newRpcFixture(t)
	first := f.requestAsync(t, "one")
	packet := f.nextLine(t)
	if packet["method"] != "one" {
		t.Fatalf("first request method = %v", packet["method"])
	}
	second := f.requestAsync(t, "two")
	packet = f.nextLine(t)
	if packet["method"] != "two" {
		t.Fatalf("second request method = %v", packet["method"])
	}
	f.feedBytes(t, "{\"id\":2,\"result\":\"你好\"}\n{\"id\":1,\"result\":1}\n")
	got := awaitResult(t, first)
	if got.err != nil || got.value != float64(1) {
		t.Fatalf("first = %v, %v", got.value, got.err)
	}
	got = awaitResult(t, second)
	if got.err != nil || got.value != "你好" {
		t.Fatalf("second = %v, %v", got.value, got.err)
	}
}

func TestServerRequestsAndNotificationsDoNotConsumeClientIds(t *testing.T) {
	f := newRpcFixture(t)
	var mu sync.Mutex
	var methods []string
	record := func(kind string, packet map[string]any) {
		name, _ := packet["method"].(string)
		mu.Lock()
		defer mu.Unlock()
		methods = append(methods, kind+":"+name)
	}
	f.rpc.SetHandlers(
		func(packet map[string]any) { record("request", packet) },
		func(packet map[string]any) { record("notify", packet) })
	pending := f.requestAsync(t, "client")
	if packet := f.nextLine(t); packet["method"] != "client" {
		t.Fatalf("client request method = %v", packet["method"])
	}
	f.feedLine(t, `{"id":1,"method":"approval"}`)
	f.feedLine(t, `{"method":"delta"}`)
	waitFor(t, "server packets delivered", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(methods) == 2
	})
	f.feedLine(t, `{"id":1,"result":true}`)
	got := awaitResult(t, pending)
	if got.err != nil || got.value != true {
		t.Fatalf("client request = %v, %v", got.value, got.err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 2 || methods[0] != "request:approval" || methods[1] != "notify:delta" {
		t.Fatalf("server methods = %v", methods)
	}
}

func TestRpcErrorDoesNotCloseConnection(t *testing.T) {
	f := newRpcFixture(t)
	bad := f.requestAsync(t, "bad")
	if packet := f.nextLine(t); packet["method"] != "bad" {
		t.Fatalf("bad request method = %v", packet["method"])
	}
	f.feedLine(t, `{"id":1,"error":{"code":9,"message":"denied"}}`)
	got := awaitResult(t, bad)
	var rpcError *RpcError
	if !errors.As(got.err, &rpcError) || rpcError.Message != "denied" || rpcError.Code == nil || *rpcError.Code != 9 {
		t.Fatalf("error = %v", got.err)
	}
	good := f.requestAsync(t, "good")
	if packet := f.nextLine(t); packet["method"] != "good" {
		t.Fatalf("good request method = %v", packet["method"])
	}
	f.feedLine(t, `{"id":2,"result":{}}`)
	got = awaitResult(t, good)
	if got.err != nil {
		t.Fatalf("healthy connection rejected: %v", got.err)
	}
	if _, ok := nativejson.Fields(got.value); !ok {
		t.Fatalf("result = %T", got.value)
	}
}

func TestMalformedInputAndEofRejectPendingAndBlockFuture(t *testing.T) {
	for _, malformed := range []bool{true, false} {
		f := newRpcFixture(t)
		pending := f.requestAsync(t, "wait")
		if malformed {
			f.feedLine(t, "invalid json")
		} else if err := f.input.Close(); err != nil {
			t.Fatalf("close input: %v", err)
		}
		got := awaitResult(t, pending)
		if got.err == nil {
			t.Fatal("pending request resolved after transport failure")
		}
		if _, err := f.rpc.Request("later", map[string]any{}); err == nil {
			t.Fatal("request accepted after transport failure")
		}
	}
}

func TestTimedOutRequestCanBeFollowedBySuccess(t *testing.T) {
	f := newRpcFixture(t)
	if _, err := f.rpc.RequestTimeout("slow", map[string]any{}, 20*time.Millisecond); err == nil ||
		!strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
	// The timed-out request was still written to the transport before it
	// expired; consume that line before asserting on the follow-up request.
	if packet := f.nextLine(t); packet["method"] != "slow" {
		t.Fatalf("timed out request method = %v", packet["method"])
	}
	next := f.requestAsync(t, "next")
	if packet := f.nextLine(t); packet["method"] != "next" {
		t.Fatalf("next request method = %v", packet["method"])
	}
	f.feedLine(t, `{"id":1,"result":"late"}`)
	f.feedLine(t, `{"id":2,"result":"ok"}`)
	got := awaitResult(t, next)
	if got.err != nil || got.value != "ok" {
		t.Fatalf("next = %v, %v", got.value, got.err)
	}
}
