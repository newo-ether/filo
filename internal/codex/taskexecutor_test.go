package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// executorTaskID is the native identity every executor fixture admits.
const executorTaskID = "00000000-0000-0000-0000-000000000001"

func executorToken() string { return strings.Repeat("a", 64) }

type executorCall struct {
	method string
	params map[string]any
}

type executorFixtureOptions struct {
	finishBeforeReceipt bool
	holdCreation        chan struct{}
	gate                chan struct{}
	admittedID          string
	protectedLifetime   func() bool
	onNativeCreated     func()
	recordTask          func(string) error
}

// executorFixture drives a TaskExecutor over a pipe pair that stands in for the
// native host, exactly as the TypeScript fixture answered through its emitter.
type executorFixture struct {
	t       *testing.T
	options executorFixtureOptions
	address string
	url     string

	executor *TaskExecutor
	client   *CodexHost

	toExecutor   *io.PipeWriter
	toNativeRead *io.PipeReader
	writeMu      sync.Mutex

	mu       sync.Mutex
	calls    []executorCall
	ids      []string
	released int
	status   string
}

func newExecutorFixture(t *testing.T, options executorFixtureOptions) *executorFixture {
	t.Helper()
	f := &executorFixture{t: t, options: options, status: "idle"}
	toExecutorReader, toExecutorWriter := io.Pipe()
	toNativeReader, toNativeWriter := io.Pipe()
	f.toExecutor, f.toNativeRead = toExecutorWriter, toNativeReader
	token := executorToken()
	var executor *TaskExecutor
	rpc := NewRpc(toExecutorReader, toNativeWriter)
	executor, err := NewTaskExecutor(rpc, TaskExecutorOptions{
		Token:      token,
		AdmittedID: options.admittedID,
		ReleaseNative: func() error {
			f.mu.Lock()
			f.released++
			f.mu.Unlock()
			// The TypeScript fixture emitted 'closed', which ran the executor's
			// own disconnect path.
			executor.Disconnect()
			return nil
		},
		RecordTask: func(id string) error {
			f.mu.Lock()
			f.ids = append(f.ids, id)
			f.mu.Unlock()
			if options.recordTask != nil {
				return options.recordTask(id)
			}
			return nil
		},
		OnNativeCreated:   options.onNativeCreated,
		Idle:              10 * time.Second,
		Completed:         30 * time.Millisecond,
		ProtectedLifetime: options.protectedLifetime,
	})
	if err != nil {
		t.Fatalf("NewTaskExecutor: %v", err)
	}
	f.executor = executor
	url, err := executor.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	f.url, f.address = url, strings.TrimPrefix(url, "ws://")
	go f.serveNative()
	client, err := ConnectCodexHost(context.Background(), url, HostOptions{Token: &token})
	if err != nil {
		t.Fatalf("ConnectCodexHost: %v", err)
	}
	f.client = client
	t.Cleanup(f.close)
	return f
}

func (f *executorFixture) close() {
	f.client.Close()
	f.executor.Disconnect()
	_ = f.toExecutor.Close()
	_ = f.toNativeRead.Close()
}

// serveNative answers one native packet per JSONL line. Each request is handled
// independently, and only forwarded requests are recorded.
func (f *executorFixture) serveNative() {
	scanner := bufio.NewScanner(f.toNativeRead)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		var packet map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &packet); err != nil {
			continue
		}
		name, _ := packet["method"].(string)
		if name == "" {
			continue // a response to a native originated callback
		}
		if _, isRequest := packet["id"]; !isRequest {
			continue // the initialized notification
		}
		params, _ := packet["params"].(map[string]any)
		f.record(executorCall{method: name, params: params})
		go f.answer(name, packet["id"])
	}
}

func (f *executorFixture) answer(name string, identifier any) {
	switch name {
	case "thread/start":
		if f.options.holdCreation != nil {
			<-f.options.holdCreation
		}
		f.reply(identifier, map[string]any{"thread": f.threadObject()})
	case "thread/resume", "thread/read":
		f.reply(identifier, map[string]any{"thread": f.threadObject()})
	case "thread/turns/list":
		status := "inProgress"
		if f.statusValue() == "systemError" {
			status = "failed"
		}
		f.reply(identifier, map[string]any{"data": []any{map[string]any{"status": status}}})
	case "turn/start":
		f.setStatus("active")
		f.notify("turn/started", map[string]any{
			"threadId": executorTaskID,
			"turn":     map[string]any{"id": "turn", "status": "inProgress"},
		})
		if f.options.finishBeforeReceipt {
			f.complete()
		}
		f.reply(identifier, map[string]any{"turn": map[string]any{"id": "turn"}})
	case "model/list":
		if f.options.gate != nil {
			<-f.options.gate
		}
		f.reply(identifier, map[string]any{})
	default:
		f.reply(identifier, map[string]any{})
	}
}

func (f *executorFixture) threadObject() map[string]any {
	return map[string]any{"id": executorTaskID, "status": map[string]any{"type": f.statusValue()}}
}

// complete ends the active turn in the native host's own words.
func (f *executorFixture) complete() {
	f.setStatus("idle")
	f.notify("turn/completed", map[string]any{
		"threadId": executorTaskID,
		"turn":     map[string]any{"id": "turn", "status": "completed"},
	})
}

// failTurn reports an authoritative native failure for the active turn.
func (f *executorFixture) failTurn() {
	f.setStatus("systemError")
	f.notify("turn/completed", map[string]any{
		"threadId": executorTaskID,
		"turn":     map[string]any{"id": "turn", "status": "failed"},
	})
}

func (f *executorFixture) reply(identifier any, result any) {
	f.write(map[string]any{"id": identifier, "result": result})
}

func (f *executorFixture) notify(name string, params map[string]any) {
	f.write(map[string]any{"method": name, "params": params})
}

func (f *executorFixture) write(packet map[string]any) {
	payload, err := json.Marshal(packet)
	if err != nil {
		return
	}
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	_, _ = f.toExecutor.Write(append(payload, '\n'))
}

func (f *executorFixture) record(call executorCall) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}

func (f *executorFixture) statusValue() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *executorFixture) setStatus(status string) {
	f.mu.Lock()
	f.status = status
	f.mu.Unlock()
}

func (f *executorFixture) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *executorFixture) callMethods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	methods := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		methods = append(methods, call.method)
	}
	return methods
}

func (f *executorFixture) countMethod(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, call := range f.calls {
		if call.method == name {
			count++
		}
	}
	return count
}

func (f *executorFixture) lastCall() (executorCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return executorCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

func (f *executorFixture) lastMethod() string {
	call, ok := f.lastCall()
	if !ok {
		return ""
	}
	return call.method
}

func (f *executorFixture) createdIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ids...)
}

func (f *executorFixture) releaseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.released
}

// rawClient dials the private endpoint directly so the handshake itself can be
// examined, including the frames the ws client library never exposed.
func (f *executorFixture) rawClient(t *testing.T, authorization, origin string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	conn, err := net.Dial("tcp", f.address)
	if err != nil {
		t.Fatalf("dial the executor: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	request := "GET /private HTTP/1.1\r\nHost: 127.0.0.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n"
	if authorization != "" {
		request += "Authorization: " + authorization + "\r\n"
	}
	if origin != "" {
		request += "Origin: " + origin + "\r\n"
	}
	request += "\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("write the handshake: %v", err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read the handshake response: %v", err)
	}
	return conn, reader, response
}

// executorSemantic converts a native value into plain JSON shapes so a fixture
// answer can be compared without depending on retained property order.
func executorSemantic(t *testing.T, value any) any {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %#v: %v", value, err)
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal %s: %v", payload, err)
	}
	return decoded
}

func executorPath(t *testing.T, value any, keys ...string) any {
	t.Helper()
	current := executorSemantic(t, value)
	for _, key := range keys {
		fields, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%v is not an object", current)
		}
		current = fields[key]
	}
	return current
}

func TestTaskExecutorFencesNewInputWhenLifetimeProtectionIsLost(t *testing.T) {
	var protected atomic.Bool
	f := newExecutorFixture(t, executorFixtureOptions{protectedLifetime: protected.Load})
	if _, err := f.client.Rpc.Request("thread/start", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "lifetime protection") {
		t.Fatalf("the fenced thread/start = %v", err)
	}
	if count := f.callCount(); count != 0 {
		t.Fatalf("the fenced thread/start reached the native host %d times", count)
	}
	protected.Store(true)
	if _, err := f.client.Rpc.Request("thread/start", map[string]any{}); err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	if _, err := f.client.Rpc.Request("turn/start", map[string]any{"threadId": executorTaskID}); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	protected.Store(false)
	if _, err := f.client.Rpc.Request("turn/steer", map[string]any{"threadId": executorTaskID}); err == nil ||
		!strings.Contains(err.Error(), "lifetime protection") {
		t.Fatalf("the fenced turn/steer = %v", err)
	}
	thread, err := f.client.Rpc.Request("thread/read", map[string]any{"threadId": executorTaskID})
	if err != nil {
		t.Fatalf("thread/read: %v", err)
	}
	if status := executorPath(t, thread, "thread", "status", "type"); status != "active" {
		t.Fatalf("the live thread status = %v", status)
	}
	if released := f.releaseCount(); released != 0 {
		t.Fatalf("an active turn released its native writer %d times", released)
	}
	f.complete()
	waitFor(t, "the completed turn to release its native writer", func() bool { return f.releaseCount() == 1 })
	if count := f.countMethod("turn/interrupt"); count != 0 {
		t.Fatalf("a lost lifetime fence interrupted the native turn %d times", count)
	}
}

func TestTaskExecutorAuthenticatesAndScopesNativeOperations(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{})
	if _, err := ConnectCodexHost(context.Background(), f.url, HostOptions{}); err == nil {
		t.Fatal("the executor admitted a client with no credential")
	}
	other := strings.Repeat("b", 64)
	if _, err := ConnectCodexHost(context.Background(), f.url, HostOptions{Token: &other}); err == nil {
		t.Fatal("the executor admitted a client with the wrong credential")
	}
	if _, err := f.client.Rpc.Request("thread/resume", map[string]any{"threadId": executorTaskID}); err == nil ||
		!strings.Contains(err.Error(), "not been admitted") {
		t.Fatalf("the dormant resume = %v", err)
	}
	if count := f.callCount(); count != 0 {
		t.Fatalf("the rejected operations reached the native host %d times", count)
	}
	if _, err := f.client.Rpc.Request("thread/start", map[string]any{}); err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	if ids := f.createdIDs(); len(ids) != 1 || ids[0] != executorTaskID {
		t.Fatalf("recorded task identities = %v", ids)
	}
	if _, err := f.client.Rpc.Request("thread/start", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "already owns") {
		t.Fatalf("the second thread/start = %v", err)
	}
	if _, err := f.client.Rpc.Request("thread/read", map[string]any{"threadId": "unrelated"}); err == nil ||
		!strings.Contains(err.Error(), "outside") {
		t.Fatalf("the unrelated read = %v", err)
	}
	if _, err := f.client.Rpc.Request("filo/task/state", map[string]any{"threadId": "unrelated"}); err == nil ||
		!strings.Contains(err.Error(), "outside") {
		t.Fatalf("the unrelated state read = %v", err)
	}
	if _, err := f.client.Rpc.Request("command/exec", map[string]any{"command": "unrequested"}); err == nil ||
		!strings.Contains(err.Error(), "outside") {
		t.Fatalf("the arbitrary native operation = %v", err)
	}
	if methods := f.callMethods(); len(methods) != 1 || methods[0] != "thread/start" {
		t.Fatalf("native methods = %v", methods)
	}
	token := executorToken()
	reconnected, err := ConnectCodexHost(context.Background(), f.url, HostOptions{Token: &token})
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if _, err := reconnected.Rpc.Request("thread/resume", map[string]any{"threadId": executorTaskID}); err != nil {
		t.Fatalf("the reconnected resume: %v", err)
	}
	reconnected.Close()
	if last := f.lastMethod(); last != "thread/resume" {
		t.Fatalf("the last native method = %q", last)
	}
	if ids := f.createdIDs(); len(ids) != 1 {
		t.Fatalf("a resume recorded another native task: %v", ids)
	}
}

func TestTaskExecutorReportsNativeAcknowledgementBeforeProvenance(t *testing.T) {
	var mu sync.Mutex
	var stages []string
	f := newExecutorFixture(t, executorFixtureOptions{
		onNativeCreated: func() {
			mu.Lock()
			stages = append(stages, "native")
			mu.Unlock()
		},
		recordTask: func(string) error {
			mu.Lock()
			stages = append(stages, "provenance")
			mu.Unlock()
			return nil
		},
	})
	if _, err := f.client.Rpc.Request("thread/start", map[string]any{}); err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	mu.Lock()
	observed := append([]string(nil), stages...)
	mu.Unlock()
	if !reflect.DeepEqual(observed, []string{"native", "provenance"}) {
		t.Fatalf("creation callback order = %v", observed)
	}
}

func TestTaskExecutorSurvivesClientDisappearanceAndVerifiesIdleBeforeRelease(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{})
	if _, err := f.client.Rpc.Request("thread/start", map[string]any{}); err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	receipt, err := f.client.Rpc.Request("turn/start", map[string]any{
		"threadId":            executorTaskID,
		"clientUserMessageId": "exact-input",
		"input":               []any{map[string]any{"type": "text", "text": "one input"}},
	})
	if err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	if id := executorPath(t, receipt, "turn", "id"); id != "turn" {
		t.Fatalf("the turn receipt = %v", id)
	}
	f.client.Close()
	if retired, err := f.executor.Retire(); retired || err != nil {
		t.Fatalf("retiring an active turn = %v, %v", retired, err)
	}
	time.Sleep(60 * time.Millisecond)
	if released := f.releaseCount(); released != 0 {
		t.Fatalf("a client disconnect released the native writer %d times", released)
	}
	f.notify("item/completed", map[string]any{
		"threadId": executorTaskID,
		"item":     map[string]any{"type": "mcpToolCall"},
	})
	f.complete()
	waitFor(t, "the completed turn to release its native writer", func() bool { return f.releaseCount() == 1 })
	if count := f.countMethod("turn/start"); count != 1 {
		t.Fatalf("turn/start reached the native host %d times", count)
	}
	if last := f.lastMethod(); last != "thread/read" {
		t.Fatalf("retirement ended with the native call %q", last)
	}
}

func TestTaskExecutorReleasesWhenCompletionPrecedesItsReceipt(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{finishBeforeReceipt: true})
	if _, err := f.client.Rpc.Request("thread/start", map[string]any{}); err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	if _, err := f.client.Rpc.Request("turn/start", map[string]any{"threadId": executorTaskID}); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	waitFor(t, "the early completion to release its native writer", func() bool { return f.releaseCount() == 1 })
}

func TestTaskExecutorFencesConcurrentCreationAndRetirement(t *testing.T) {
	unblock := make(chan struct{})
	f := newExecutorFixture(t, executorFixtureOptions{holdCreation: unblock})
	creation := make(chan error, 1)
	go func() {
		_, err := f.client.Rpc.Request("thread/start", map[string]any{})
		creation <- err
	}()
	waitFor(t, "the held native creation", func() bool { return f.callCount() == 1 })
	if retired, err := f.executor.Retire(); retired || err != nil {
		t.Fatalf("retiring during creation = %v, %v", retired, err)
	}
	if _, err := f.client.Rpc.Request("thread/start", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "already owns") {
		t.Fatalf("the concurrent second creation = %v", err)
	}
	close(unblock)
	if err := <-creation; err != nil {
		t.Fatalf("the held creation: %v", err)
	}
	if ids := f.createdIDs(); len(ids) != 1 || ids[0] != executorTaskID {
		t.Fatalf("recorded task identities = %v", ids)
	}
	if count := f.countMethod("thread/start"); count != 1 {
		t.Fatalf("thread/start reached the native host %d times", count)
	}
	if retired, err := f.executor.Retire(); !retired || err != nil {
		t.Fatalf("retiring the idle task = %v, %v", retired, err)
	}
	if released := f.releaseCount(); released != 1 {
		t.Fatalf("releases = %d", released)
	}
}

func TestTaskExecutorVerifiesFailedTurnsWithoutAnIndexQuery(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{})
	if _, err := f.client.Rpc.Request("thread/start", map[string]any{}); err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	if _, err := f.client.Rpc.Request("turn/start", map[string]any{"threadId": executorTaskID}); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	f.client.Close()
	f.failTurn()
	waitFor(t, "the failed turn to release its native writer", func() bool { return f.releaseCount() == 1 })
	if last := f.lastMethod(); last != "thread/read" {
		t.Fatalf("retirement ended with the native call %q", last)
	}
	if count := f.countMethod("thread/turns/list"); count != 0 {
		t.Fatalf("the executor used an unsupported active index query %d times", count)
	}
}

func TestTaskExecutorKeepsScopedStateAcrossClientReplacement(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{})
	if _, err := f.client.Rpc.Request("thread/start", map[string]any{}); err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	initial, err := f.client.Rpc.Request("filo/task/state", map[string]any{"threadId": executorTaskID})
	if err != nil {
		t.Fatalf("the initial state: %v", err)
	}
	if got := executorSemantic(t, initial); !reflect.DeepEqual(got, map[string]any{"hasUser": false, "materialized": false}) {
		t.Fatalf("the initial state = %#v", got)
	}
	if _, err := f.client.Rpc.Request("turn/start", map[string]any{"threadId": executorTaskID}); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	f.notify("item/completed", map[string]any{
		"threadId": executorTaskID,
		"turnId":   "turn",
		"item":     map[string]any{"type": "userMessage"},
	})
	token := executorToken()
	replacement, err := ConnectCodexHost(context.Background(), f.url, HostOptions{Token: &token})
	if err != nil {
		t.Fatalf("replacement connect: %v", err)
	}
	defer replacement.Close()
	turn, err := replacement.Rpc.Request("filo/task/state", map[string]any{"threadId": executorTaskID})
	if err != nil {
		t.Fatalf("the retained state: %v", err)
	}
	want := map[string]any{
		"turn":         map[string]any{"id": "turn", "status": "inProgress"},
		"hasUser":      true,
		"materialized": true,
	}
	if got := executorSemantic(t, turn); !reflect.DeepEqual(got, want) {
		t.Fatalf("the retained state = %#v", got)
	}
	if count := f.countMethod("thread/turns/list"); count != 0 {
		t.Fatalf("the runtime state queried the native index %d times", count)
	}
}

func TestTaskExecutorRefusesADifferentNativeWriter(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{admittedID: executorTaskID})
	if _, err := f.client.Rpc.Request("thread/start", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "already owns") {
		t.Fatalf("the admitted executor accepted thread/start: %v", err)
	}
	if _, err := f.client.Rpc.Request("thread/resume", map[string]any{
		"threadId": "00000000-0000-0000-0000-000000000002",
	}); err == nil || !strings.Contains(err.Error(), "already owns") {
		t.Fatalf("the admitted executor accepted a different resume: %v", err)
	}
	if count := f.callCount(); count != 0 {
		t.Fatalf("the refused operations reached the native host %d times", count)
	}
	if _, err := f.client.Rpc.Request("thread/resume", map[string]any{"threadId": executorTaskID}); err != nil {
		t.Fatalf("the admitted resume: %v", err)
	}
	call, ok := f.lastCall()
	if !ok || call.method != "thread/resume" {
		t.Fatalf("the last native call = %+v", call)
	}
	if !reflect.DeepEqual(executorSemantic(t, call.params), map[string]any{"threadId": executorTaskID}) {
		t.Fatalf("the resumed params = %#v", call.params)
	}
	if ids := f.createdIDs(); len(ids) != 0 {
		t.Fatalf("an admitted executor recorded another task: %v", ids)
	}
}

func TestTaskExecutorRejectsWorkBeyondItsPendingBound(t *testing.T) {
	gate := make(chan struct{})
	f := newExecutorFixture(t, executorFixtureOptions{gate: gate})
	admitted := make(chan error, executorMaxPending)
	for index := 0; index < executorMaxPending; index++ {
		go func() {
			_, err := f.client.Rpc.Request("model/list", map[string]any{})
			admitted <- err
		}()
	}
	waitFor(t, "every pending slot to fill", func() bool { return f.callCount() == executorMaxPending })
	if _, err := f.client.Rpc.Request("model/list", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "retiring or busy") {
		t.Fatalf("the request beyond the pending bound = %v", err)
	}
	if count := f.callCount(); count != executorMaxPending {
		t.Fatalf("the refused request still reached the native host: %d calls", count)
	}
	close(gate)
	for index := 0; index < executorMaxPending; index++ {
		if err := <-admitted; err != nil {
			t.Fatalf("an admitted request failed: %v", err)
		}
	}
}

func TestDecoderAdmitsExecutorPacketsInTheNativeOrder(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		ok     bool
		method string
		hasID  bool
	}{
		{"notification", `{"method":"initialized","params":{}}`, true, "initialized", false},
		{"numeric id", `{"method":"thread/read","id":7,"params":{}}`, true, "thread/read", true},
		{"string id", `{"method":"thread/read","id":"7"}`, true, "thread/read", true},
		{"missing method", `{"id":1}`, false, "", false},
		{"non string method", `{"method":1,"id":1}`, false, "", false},
		{"boolean id", `{"method":"thread/read","id":true}`, false, "", false},
		{"object id", `{"method":"thread/read","id":{}}`, false, "", false},
		{"array envelope", `[]`, false, "", false},
		{"malformed", `{"method":`, false, "", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			packet, ok := decodeExecutorPacket(test.text)
			if ok != test.ok {
				t.Fatalf("ok = %v", ok)
			}
			if !ok {
				return
			}
			if packet.method != test.method || packet.hasID != test.hasID {
				t.Fatalf("packet = %+v", packet)
			}
		})
	}
}

func TestTaskExecutorRejectsUnknownClientsAtTheHandshake(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{})
	cases := []struct {
		name          string
		authorization string
		origin        string
	}{
		{"no credential", "", ""},
		{"wrong credential", "Bearer " + strings.Repeat("b", 64), ""},
		{"browser origin", "Bearer " + executorToken(), "http://localhost"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, _, response := f.rawClient(t, test.authorization, test.origin)
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d", response.StatusCode)
			}
		})
	}
}

func TestTaskExecutorClosesAnInvalidPacketWithAProtocolCode(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{})
	conn, reader, response := f.rawClient(t, "Bearer "+executorToken(), "")
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", response.StatusCode)
	}
	writeRawFrame(t, conn, rawFrame{final: true, opcode: opText, masked: true, payload: []byte(`{"method":7,"id":1}`)})
	frame, err := readRawFrame(t, reader)
	if err != nil {
		t.Fatalf("read the close frame: %v", err)
	}
	if frame.opcode != opClose || len(frame.payload) < 2 ||
		binary.BigEndian.Uint16(frame.payload[:2]) != 1008 || string(frame.payload[2:]) != "Invalid request" {
		t.Fatalf("the executor answered %+v", frame)
	}
}

func TestTaskExecutorAcceptsAnInitializationNotification(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{})
	conn, reader, response := f.rawClient(t, "Bearer "+executorToken(), "")
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", response.StatusCode)
	}
	writeRawFrame(t, conn, rawFrame{final: true, opcode: opText, masked: true, payload: []byte(`{"method":"initialized","params":{}}`)})
	writeRawFrame(t, conn, rawFrame{final: true, opcode: opText, masked: true, payload: []byte(`{"method":"model/list","id":1,"params":{}}`)})
	frame, err := readRawFrame(t, reader)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	if frame.opcode != opText {
		t.Fatalf("the executor answered opcode %d", frame.opcode)
	}
	var answer any
	if err := json.Unmarshal(frame.payload, &answer); err != nil {
		t.Fatalf("decode %q: %v", frame.payload, err)
	}
	want := map[string]any{"id": float64(1), "result": map[string]any{}}
	if !reflect.DeepEqual(answer, want) {
		t.Fatalf("the answer = %#v", answer)
	}
}

func TestTaskExecutorDropsAnOversizedClientPacket(t *testing.T) {
	f := newExecutorFixture(t, executorFixtureOptions{})
	headers := http.Header{"Authorization": {"Bearer " + executorToken()}}
	socket, err := DialWebSocket(context.Background(), f.url, headers, 5*time.Second)
	if err != nil {
		t.Fatalf("dial the executor: %v", err)
	}
	defer socket.Terminate()
	_ = socket.WriteText(bytes.Repeat([]byte("a"), executorMaxPayload+1))
	ended := make(chan error, 1)
	go func() {
		_, err := socket.ReadMessage()
		ended <- err
	}()
	select {
	case err := <-ended:
		if err == nil {
			t.Fatal("the executor assembled a packet beyond its payload bound")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the executor kept an oversized client packet alive")
	}
}
