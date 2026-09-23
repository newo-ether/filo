package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/history"
	"github.com/newo-ether/filo/internal/protocol"
)

// The two identities the TypeScript fixture pinned.
const (
	taskSessionsTaskID = "11111111-1111-4111-8111-111111111111"
	taskSessionsTurnID = "22222222-2222-4222-8222-222222222222"
)

// taskSessionsCall is one request a scripted native peer received.
type taskSessionsCall struct {
	method string
	params map[string]any
}

// taskSessionsNative is one scripted native peer behind a real JSONL transport. It
// answers the exact methods the scoped adapter drives and reproduces every state
// the TypeScript fixture scripted: a task that is admitted by its own creation, a
// turn that becomes active, a completion that supersedes a lost notification, and
// the two refusals the adapter must never retry.
type taskSessionsNative struct {
	mu        sync.Mutex
	recorded  []taskSessionsCall
	active    bool
	owned     bool
	finished  bool
	failSend  bool
	failSteer bool

	writeMu sync.Mutex
	read    *io.PipeReader
	write   *io.PipeWriter
	done    chan struct{}
}

func newTaskSessionsPeer() (*codex.Rpc, *taskSessionsNative) {
	clientInput, serverOutput := io.Pipe()
	serverInput, clientOutput := io.Pipe()
	peer := &taskSessionsNative{read: serverInput, write: serverOutput, done: make(chan struct{})}
	rpc := codex.NewRpc(clientInput, clientOutput)
	go peer.serve()
	return rpc, peer
}

// serve answers one JSONL request at a time, exactly like the shared host: a
// notification the answer pushes is written before that answer.
func (peer *taskSessionsNative) serve() {
	defer close(peer.done)
	reader := bufio.NewReader(peer.read)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		var packet map[string]any
		if err := json.Unmarshal([]byte(line), &packet); err != nil {
			return
		}
		id, request := packet["id"]
		if !request {
			continue
		}
		method, _ := packet["method"].(string)
		params, _ := packet["params"].(map[string]any)
		peer.record(method, params)
		result, failure := peer.answer(method, params)
		response := map[string]any{"id": id, "result": result}
		if failure != nil {
			response = map[string]any{"id": id,
				"error": map[string]any{"code": -32000, "message": failure.Error()}}
		}
		if peer.send(response) != nil {
			return
		}
	}
}

// answer scripts one native request: the create, resume, read and task-state
// projections the adapter consumes, plus the turn methods it drives.
func (peer *taskSessionsNative) answer(method string, params map[string]any) (any, error) {
	peer.mu.Lock()
	active, finished := peer.active, peer.finished
	failSend, failSteer := peer.failSend, peer.failSteer
	peer.mu.Unlock()
	switch method {
	case "thread/start":
		peer.mu.Lock()
		peer.owned = true
		peer.mu.Unlock()
		return map[string]any{
			"thread": map[string]any{"id": taskSessionsTaskID, "name": nil, "preview": "",
				"cwd": "/fixture", "updatedAt": 1},
			"model": "m",
		}, nil
	case "thread/resume":
		return map[string]any{"model": "m"}, nil
	case "thread/read":
		status := "idle"
		if active {
			status = "active"
		}
		return map[string]any{"thread": map[string]any{"id": taskSessionsTaskID, "model": "m",
			"status": map[string]any{"type": status}}}, nil
	case "filo/task/state":
		materialized := active || finished
		state := map[string]any{"materialized": materialized, "hasUser": materialized}
		if materialized {
			turnStatus := "inProgress"
			if !active {
				turnStatus = "completed"
			}
			state["turn"] = map[string]any{"id": taskSessionsTurnID, "status": turnStatus}
		}
		return state, nil
	case "model/list":
		return map[string]any{"data": []any{map[string]any{"model": "m", "displayName": "M",
			"isDefault": true, "supportedReasoningEfforts": []any{
				map[string]any{"reasoningEffort": "low"},
				map[string]any{"reasoningEffort": "high"},
			}}}}, nil
	case "thread/settings/update":
		return map[string]any{}, nil
	case "turn/start":
		peer.mu.Lock()
		peer.active = true
		peer.mu.Unlock()
		clientID, _ := params["clientUserMessageId"].(string)
		peer.notify("turn/started", map[string]any{"threadId": taskSessionsTaskID,
			"turn": map[string]any{"id": taskSessionsTurnID, "status": "inProgress", "startedAt": 1}})
		peer.notify("item/completed", map[string]any{"threadId": taskSessionsTaskID,
			"turnId": taskSessionsTurnID,
			"item": map[string]any{"id": "u", "type": "userMessage", "clientId": clientID,
				"content": []any{map[string]any{"type": "text", "text": "hello"}}}})
		if failSend {
			return nil, errors.New("Uncertain native receipt")
		}
		return map[string]any{"turn": map[string]any{"id": taskSessionsTurnID}}, nil
	case "turn/steer":
		if failSteer {
			return nil, errors.New("Native active turn changed")
		}
		return map[string]any{"turnId": taskSessionsTurnID}, nil
	case "turn/interrupt":
		peer.mu.Lock()
		peer.active = false
		peer.mu.Unlock()
		return map[string]any{}, nil
	}
	return nil, errors.New("Unexpected native API: " + method)
}

func (peer *taskSessionsNative) record(method string, params map[string]any) {
	peer.mu.Lock()
	peer.recorded = append(peer.recorded, taskSessionsCall{method: method, params: params})
	peer.mu.Unlock()
}

func (peer *taskSessionsNative) send(packet any) error {
	peer.writeMu.Lock()
	defer peer.writeMu.Unlock()
	return json.NewEncoder(peer.write).Encode(packet)
}

func (peer *taskSessionsNative) notify(method string, params map[string]any) {
	_ = peer.send(map[string]any{"method": method, "params": params})
}

func (peer *taskSessionsNative) calls() []taskSessionsCall {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	return append([]taskSessionsCall(nil), peer.recorded...)
}

func (peer *taskSessionsNative) callsFor(method string) []taskSessionsCall {
	var matching []taskSessionsCall
	for _, call := range peer.calls() {
		if call.method == method {
			matching = append(matching, call)
		}
	}
	return matching
}

func (peer *taskSessionsNative) callCount(method string) int { return len(peer.callsFor(method)) }

func (peer *taskSessionsNative) totalCalls() int { return len(peer.calls()) }

func (peer *taskSessionsNative) lastMethod() string {
	calls := peer.calls()
	if len(calls) == 0 {
		return ""
	}
	return calls[len(calls)-1].method
}

func (peer *taskSessionsNative) clearCalls() {
	peer.mu.Lock()
	peer.recorded = nil
	peer.mu.Unlock()
}

func (peer *taskSessionsNative) isActive() bool {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	return peer.active
}

func (peer *taskSessionsNative) isOwned() bool {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	return peer.owned
}

// failNextSend makes the next accepted turn report an unconfirmed receipt after it
// already delivered its own notifications, which is the case the adapter must not
// replay.
func (peer *taskSessionsNative) failNextSend() {
	peer.mu.Lock()
	peer.failSend = true
	peer.mu.Unlock()
}

func (peer *taskSessionsNative) rejectSteer() {
	peer.mu.Lock()
	peer.failSteer = true
	peer.mu.Unlock()
}

// finish ends the scripted turn without ever notifying the client, which is how a
// missed completion notification is reproduced.
func (peer *taskSessionsNative) finish() {
	peer.mu.Lock()
	peer.active, peer.finished = false, true
	peer.mu.Unlock()
}

// taskSessionsTestConnection is one scripted admitted connection: a real JSONL
// transport plus the close that ends this client, which is what the TypeScript
// fixture's `close` did by emitting the transport's closed event.
type taskSessionsTestConnection struct {
	rpc *codex.Rpc
}

func (connection *taskSessionsTestConnection) Transport() *codex.Rpc { return connection.rpc }
func (connection *taskSessionsTestConnection) Close()                { connection.rpc.Close() }

// taskSessionsTestFactory is the scoped executor factory of one fixture. Its
// creation provenance is the scripted peer's own admission, exactly like the
// TypeScript fixture shared one `owned` flag between the factory and the native
// port, and an executor launch is replaced by the scripted connection.
type taskSessionsTestFactory struct {
	peer       *taskSessionsNative
	connection TaskSessionsConnection

	mu          sync.Mutex
	owns        *bool
	ownsErr     error
	opens       int
	follows     int
	hold        chan struct{}
	entered     chan struct{}
	enteredOnce sync.Once
}

// holdOpen keeps the next open inside executor startup until releaseOpen, which is
// what the TypeScript test overrode the factory for.
func (factory *taskSessionsTestFactory) holdOpen() {
	factory.mu.Lock()
	factory.hold = make(chan struct{})
	factory.entered = make(chan struct{})
	factory.enteredOnce = sync.Once{}
	factory.mu.Unlock()
}

// opening returns the channel that closes once the next open was entered.
func (factory *taskSessionsTestFactory) opening() <-chan struct{} {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.entered
}

func (factory *taskSessionsTestFactory) releaseOpen() {
	factory.mu.Lock()
	hold := factory.hold
	factory.hold = nil
	factory.mu.Unlock()
	if hold != nil {
		close(hold)
	}
}

func (factory *taskSessionsTestFactory) Owns(taskID string) (bool, error) {
	factory.mu.Lock()
	forced, forcedErr := factory.owns, factory.ownsErr
	factory.mu.Unlock()
	if forcedErr != nil {
		return false, forcedErr
	}
	if forced != nil {
		return *forced, nil
	}
	return factory.peer.isOwned() && taskID == taskSessionsTaskID, nil
}

func (factory *taskSessionsTestFactory) setOwns(owned bool, err error) {
	factory.mu.Lock()
	factory.owns = &owned
	factory.ownsErr = err
	factory.mu.Unlock()
}

func (factory *taskSessionsTestFactory) Open(_ context.Context, taskID *string) (TaskSessionsConnection, error) {
	factory.mu.Lock()
	hold, entered, once := factory.hold, factory.entered, &factory.enteredOnce
	factory.mu.Unlock()
	if hold != nil {
		once.Do(func() { close(entered) })
		<-hold
	}
	if taskID != nil && (!factory.peer.isOwned() || *taskID != taskSessionsTaskID) {
		return nil, errors.New("Task is outside Filo creation scope")
	}
	factory.mu.Lock()
	factory.opens++
	factory.mu.Unlock()
	return factory.connection, nil
}

func (factory *taskSessionsTestFactory) Follow(_ context.Context, taskID string) (TaskSessionsConnection, error) {
	factory.mu.Lock()
	factory.follows++
	factory.mu.Unlock()
	if factory.peer.isOwned() && taskID == taskSessionsTaskID {
		return factory.connection, nil
	}
	return nil, nil
}

func (factory *taskSessionsTestFactory) Close() {}

func (factory *taskSessionsTestFactory) counts() (opens, follows int) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.opens, factory.follows
}

// taskSessionsRead records one read-only history read, so a test can prove that a
// browsing page was served unchanged and that a status read never hydrates.
type taskSessionsRead struct {
	id       string
	cursor   string
	activity bool
	excluded []string
}

// taskSessionsTestHistory is the read-only native history of one fixture: one user
// message with a next cursor, plus the two failures the adapter must distinguish.
type taskSessionsTestHistory struct {
	mu          sync.Mutex
	reads       []taskSessionsRead
	failing     bool
	pending     bool
	replacement []protocol.Message
	replaced    bool
}

func (reader *taskSessionsTestHistory) List(context.Context, string) (protocol.SessionPage, error) {
	return protocol.SessionPage{Sessions: []protocol.Session{}}, nil
}

func (reader *taskSessionsTestHistory) Read(_ context.Context, id, cursor string, activity bool,
	excluded []string) (protocol.ConversationPage, error) {
	reader.mu.Lock()
	reader.reads = append(reader.reads, taskSessionsRead{id: id, cursor: cursor,
		activity: activity, excluded: excluded})
	failing, pending := reader.failing, reader.pending
	replacement, replaced := reader.replacement, reader.replaced
	reader.mu.Unlock()
	if failing {
		return protocol.ConversationPage{}, errors.New("Invalid native storage")
	}
	if pending {
		return protocol.ConversationPage{}, &history.NativeTaskNotIndexed{TaskID: id}
	}
	messages := []protocol.Message{{
		MessageIdentity: protocol.MessageIdentity{ID: "u", TurnID: taskSessionsTurnID,
			ClientID: pointerTo("client"), Role: "user", Timestamp: protocol.Number(1000)},
		Text: protocol.Text("hello"),
	}}
	if replaced {
		messages = replacement
	}
	return protocol.ConversationPage{Messages: messages, Queued: []protocol.QueuedInput{},
		NextCursor: pointerTo("native-cursor")}, nil
}

func (reader *taskSessionsTestHistory) history() []taskSessionsRead {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return append([]taskSessionsRead(nil), reader.reads...)
}

// pendingIndex reproduces a newly accepted native turn whose asynchronous catalog
// publication has not landed yet.
func (reader *taskSessionsTestHistory) pendingIndex() {
	reader.mu.Lock()
	reader.pending = true
	reader.mu.Unlock()
}

func (reader *taskSessionsTestHistory) failReads() {
	reader.mu.Lock()
	reader.failing = true
	reader.mu.Unlock()
}

// complete replaces the persisted page, which is the completed native history that
// must supersede a partial live delta.
func (reader *taskSessionsTestHistory) complete(messages []protocol.Message) {
	reader.mu.Lock()
	reader.replacement, reader.replaced = messages, true
	reader.mu.Unlock()
}

// taskSessionsTestMetadata is the peripheral native surface of one fixture.
type taskSessionsTestMetadata struct {
	mu        sync.Mutex
	shutdowns int
}

func (metadata *taskSessionsTestMetadata) Models() ([]protocol.Model, error) {
	return []protocol.Model{}, nil
}

func (metadata *taskSessionsTestMetadata) Rename(id, name string) (protocol.Session, error) {
	return protocol.Session{ID: id, Title: protocol.Text("renamed"), Cwd: "/fixture", UpdatedAt: 2}, nil
}

func (metadata *taskSessionsTestMetadata) Archive(string) (string, error) { return "/fixture", nil }

func (metadata *taskSessionsTestMetadata) PrepareShutdown() error {
	metadata.mu.Lock()
	metadata.shutdowns++
	metadata.mu.Unlock()
	return nil
}

// taskSessionsFixture is one scoped adapter over scripted ports.
type taskSessionsFixture struct {
	sessions *TaskSessions
	factory  *taskSessionsTestFactory
	peer     *taskSessionsNative
	history  *taskSessionsTestHistory
	metadata *taskSessionsTestMetadata
	rpc      *codex.Rpc

	mu      sync.Mutex
	changed []string
}

func newTaskSessionsFixture(t *testing.T) *taskSessionsFixture {
	t.Helper()
	rpc, peer := newTaskSessionsPeer()
	factory := &taskSessionsTestFactory{peer: peer, connection: &taskSessionsTestConnection{rpc: rpc}}
	reader := &taskSessionsTestHistory{}
	metadata := &taskSessionsTestMetadata{}
	fixture := &taskSessionsFixture{
		sessions: NewTaskSessionsWith(metadata, reader, factory),
		factory:  factory,
		peer:     peer,
		history:  reader,
		metadata: metadata,
		rpc:      rpc,
	}
	fixture.sessions.SetChangedHandler(func(id string) {
		fixture.mu.Lock()
		fixture.changed = append(fixture.changed, id)
		fixture.mu.Unlock()
	})
	t.Cleanup(func() {
		fixture.sessions.Close()
		rpc.Close()
		select {
		case <-peer.done:
		case <-time.After(5 * time.Second):
			t.Errorf("the scripted native peer did not stop")
		}
	})
	return fixture
}

// settle waits until this transport delivered everything the peer already wrote.
// Notification handlers run on the transport's reader goroutine, so a round trip
// issued afterwards observes them. The TypeScript fixture needed no such step,
// because it emitted its events on the same call stack.
func (fixture *taskSessionsFixture) settle(t *testing.T) {
	t.Helper()
	if _, err := fixture.rpc.Request("thread/read", map[string]any{"threadId": taskSessionsTaskID}); err != nil {
		t.Fatalf("settle: %v", err)
	}
}

func (fixture *taskSessionsFixture) changes() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string(nil), fixture.changed...)
}

func taskSessionsTexts(messages []protocol.Message) []string {
	texts := make([]string, 0, len(messages))
	for _, message := range messages {
		texts = append(texts, string(message.Text))
	}
	return texts
}

func taskSessionsSameTexts(t *testing.T, messages []protocol.Message, want ...string) {
	t.Helper()
	got := taskSessionsTexts(messages)
	if len(got) != len(want) {
		t.Fatalf("texts = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("texts = %v, want %v", got, want)
		}
	}
}

// taskSessionsParams renders the params of one recorded request, so a test can pin
// the exact native call. Go marshals map keys in sorted order.
func taskSessionsParams(t *testing.T, call taskSessionsCall) string {
	t.Helper()
	encoded, err := json.Marshal(call.params)
	if err != nil {
		t.Fatalf("encode params: %v", err)
	}
	return string(encoded)
}
