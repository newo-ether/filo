package codex

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
)

// Bounds for one private task executor.
const (
	// executorMaxPayload is the ws maxPayload option: one client packet.
	executorMaxPayload = 1024 * 1024
	// executorMaxPending bounds concurrent in-flight client requests.
	executorMaxPending = 32
	// executorDefaultIdle retires an idle native task, and executorDefaultCompleted
	// retires one whose turn just reached a terminal state.
	executorDefaultIdle      = 120 * time.Second
	executorDefaultCompleted = 500 * time.Millisecond
	// executorWriteTimeout replaces the bufferedAmount guard: a Go write reaches
	// the kernel synchronously, so an unresponsive client is bounded by a
	// deadline instead of by a user-space queue length.
	executorWriteTimeout = 5 * time.Second
)

var (
	executorUUIDPattern  = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	executorDeltaPattern = regexp.MustCompile(`(?:/delta|Delta)$`)
)

var executorThreadMethods = map[string]bool{
	"thread/read": true, "thread/turns/list": true, "thread/items/list": true,
	"thread/queue/list": true, "thread/settings/update": true, "thread/name/set": true,
	"thread/archive": true, "turn/start": true, "turn/steer": true, "turn/interrupt": true,
}

var executorTerminalMethods = map[string]bool{
	"turn/completed": true, "thread/archived": true, "thread/closed": true,
}

var executorTerminalTurnStatuses = map[string]bool{
	"failed": true, "interrupted": true, "completed": true,
}

type executorTurn struct {
	id     string
	status string
}

// TaskExecutorOptions carries the private credential and the native lifetime
// callbacks for one executor.
//
// Divergence from the TypeScript constructor: a Go Duration cannot distinguish an
// omitted argument from zero, so Idle == 0 and Completed == 0 select the defaults
// instead of immediate retirement. Use a small positive duration for that.
type TaskExecutorOptions struct {
	// Token is the 64 hex character private executor credential.
	Token string
	// AdmittedID is the durably admitted task this executor resumes. Empty means
	// no task has been admitted yet.
	AdmittedID string
	// ReleaseNative releases this executor's native writer.
	ReleaseNative func() error
	// RecordTask persists the newly created native task identity.
	RecordTask func(id string) error
	// OnNativeCreated observes a valid native thread/start response before
	// creation provenance is recorded. It must not affect the native result.
	OnNativeCreated func()
	// Idle retires a task with no client traffic. Zero selects 120 seconds.
	Idle time.Duration
	// Completed retires a task whose turn just completed. Zero selects 500ms.
	Completed time.Duration
	// ProtectedLifetime reports whether native task lifetime protection is
	// available. Nil means it always is.
	ProtectedLifetime func() bool
}

// TaskExecutor owns one native task and one private loopback endpoint. Its
// lifetime is independent of the gateway clients that connect to it.
type TaskExecutor struct {
	rpc               *Rpc
	expected          []byte
	releaseNative     func() error
	recordTask        func(id string) error
	onNativeCreated   func()
	protectedLifetime func() bool
	idle              time.Duration
	completed         time.Duration

	mu    sync.Mutex
	id    string
	turn  *executorTurn
	timer *time.Timer

	hasUser      bool
	materialized bool
	pending      int
	active       bool
	settled      bool
	retiring     bool
	opening      bool

	server    *http.Server
	sockets   map[*WebSocket]struct{}
	onRetired func()
	onFailure func(error)
}

// NewTaskExecutor validates the credential and admitted identity, and attaches
// this executor to a native host connection.
func NewTaskExecutor(rpc *Rpc, options TaskExecutorOptions) (*TaskExecutor, error) {
	if !privateTokenPattern.MatchString(options.Token) {
		return nil, errors.New("Invalid private executor credential")
	}
	if options.AdmittedID != "" && !executorUUIDPattern.MatchString(options.AdmittedID) {
		return nil, errors.New("Invalid admitted task identity")
	}
	executor := &TaskExecutor{
		rpc:               rpc,
		expected:          []byte("Bearer " + options.Token),
		releaseNative:     options.ReleaseNative,
		recordTask:        options.RecordTask,
		onNativeCreated:   options.OnNativeCreated,
		idle:              options.Idle,
		completed:         options.Completed,
		id:                options.AdmittedID,
		materialized:      options.AdmittedID != "",
		sockets:           make(map[*WebSocket]struct{}),
		protectedLifetime: options.ProtectedLifetime,
	}
	if executor.idle <= 0 {
		executor.idle = executorDefaultIdle
	}
	if executor.completed <= 0 {
		executor.completed = executorDefaultCompleted
	}
	if executor.releaseNative == nil {
		executor.releaseNative = func() error { return nil }
	}
	if executor.recordTask == nil {
		executor.recordTask = func(string) error { return nil }
	}
	if executor.onNativeCreated == nil {
		executor.onNativeCreated = func() {}
	}
	if executor.protectedLifetime == nil {
		executor.protectedLifetime = func() bool { return true }
	}
	// Native MCP tools execute inside the unchanged runtime. Unsupported approval
	// or input requests must fail explicitly.
	rpc.SetHandlers(executor.handleRequest, executor.handleNotification)
	rpc.SetClosedHandler(func(error) { executor.Disconnect() })
	return executor, nil
}

// SetOnRetired installs the callback that runs after a successful retirement.
func (e *TaskExecutor) SetOnRetired(callback func()) {
	e.mu.Lock()
	e.onRetired = callback
	e.mu.Unlock()
}

// SetOnFailure installs the callback for a background retirement failure.
func (e *TaskExecutor) SetOnFailure(callback func(error)) {
	e.mu.Lock()
	e.onFailure = callback
	e.mu.Unlock()
}

// Listen binds the private loopback endpoint and returns its WebSocket URL.
func (e *TaskExecutor) Listen() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return "", errors.New("Private executor did not bind")
	}
	server := &http.Server{Handler: http.HandlerFunc(e.serveClient)}
	e.mu.Lock()
	e.server = server
	e.mu.Unlock()
	go func() { _ = server.Serve(listener) }()
	e.schedule()
	return fmt.Sprintf("ws://127.0.0.1:%d", address.Port), nil
}

func (e *TaskExecutor) serveClient(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Origin") != "" || !e.authorized(request.Header.Get("Authorization")) {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	socket, err := UpgradeWebSocket(writer, request)
	if err != nil {
		return
	}
	socket.LimitMessage(executorMaxPayload)
	e.trackSocket(socket, true)
	defer e.trackSocket(socket, false)
	for {
		message, err := socket.ReadMessage()
		if err != nil {
			// The TypeScript listener terminated its socket on any transport
			// error, which also covers an oversized or protocol-violating
			// packet that never reached a handler.
			socket.Terminate()
			return
		}
		// Each client request is handled independently, exactly as the
		// TypeScript listener dispatched one async handler per message.
		go e.receive(socket, string(message))
	}
}

// authorized mirrors verifyClient: no Origin header, and a constant-time
// comparison of the exact bearer credential.
func (e *TaskExecutor) authorized(header string) bool {
	actual := []byte(header)
	return len(actual) == len(e.expected) && subtle.ConstantTimeCompare(actual, e.expected) == 1
}

type executorPacket struct {
	method string
	params any
	id     any
	hasID  bool
}

// decodeExecutorPacket applies the same admission order as the TypeScript
// listener: a method is required, an id may only be a string or a number, and an
// initialization notification is recognized before the id check.
func decodeExecutorPacket(text string) (executorPacket, bool) {
	message, _, err := nativejson.Read(strings.NewReader(text), false)
	if err != nil {
		return executorPacket{}, false
	}
	fields, ok := nativejson.Fields(message)
	if !ok {
		return executorPacket{}, false
	}
	method, ok := fields["method"].(string)
	if !ok {
		return executorPacket{}, false
	}
	packet := executorPacket{method: method, params: fields["params"]}
	id, hasID := fields["id"]
	if !hasID {
		return packet, true
	}
	switch id.(type) {
	case string, float64:
		packet.id, packet.hasID = id, true
		return packet, true
	default:
		return executorPacket{}, false
	}
}

func (e *TaskExecutor) receive(socket *WebSocket, text string) {
	packet, ok := decodeExecutorPacket(text)
	if !ok {
		socket.CloseWith(1008, "Invalid request")
		return
	}
	if packet.method == "initialized" && !packet.hasID {
		return
	}
	if !packet.hasID {
		socket.CloseWith(1008, "Invalid request")
		return
	}
	e.mu.Lock()
	busy := e.retiring || e.pending >= executorMaxPending
	if !busy {
		e.pending++
		e.stopTimerLocked()
	}
	e.mu.Unlock()
	if busy {
		e.write(socket, map[string]any{"id": packet.id, "error": map[string]any{
			"code": -32600, "message": "Native executor is retiring or busy",
		}})
		return
	}
	result, err := e.request(packet.method, packet.params)
	if err != nil {
		code, message := executorFailure(err)
		e.write(socket, map[string]any{"id": packet.id, "error": map[string]any{"code": code, "message": message}})
	} else {
		e.write(socket, map[string]any{"id": packet.id, "result": result})
	}
	e.mu.Lock()
	e.pending--
	e.mu.Unlock()
	e.schedule()
}

func executorFailure(err error) (float64, string) {
	var rpcError *RpcError
	if errors.As(err, &rpcError) {
		if rpcError.Code != nil {
			return *rpcError.Code, rpcError.Message
		}
		// The native error object omitted a code, which the TypeScript client
		// serialized away entirely; a bounded code keeps this wire valid.
		return -32000, rpcError.Message
	}
	if err != nil {
		return -32000, err.Error()
	}
	return -32000, "Native task operation failed"
}

func rejectExecutor(message string) error {
	code := float64(-32600)
	return &RpcError{Message: message, Code: &code}
}

func (e *TaskExecutor) request(method string, params any) (any, error) {
	if method == "initialize" {
		return map[string]any{"userAgent": "filo-private-task-executor"}, nil
	}
	if method == "thread/start" || method == "turn/start" || method == "turn/steer" {
		if !e.protectedLifetime() {
			return nil, rejectExecutor("Native task lifetime protection is unavailable")
		}
	}
	fields, _ := nativejson.Fields(params)
	threadID := executorText(fields["threadId"])
	if method == "filo/task/state" {
		e.mu.Lock()
		owned := e.id
		turn := e.turn
		state := map[string]any{"hasUser": e.hasUser, "materialized": e.materialized}
		e.mu.Unlock()
		if owned == "" || threadID != owned {
			return nil, rejectExecutor("Task is outside this executor")
		}
		if turn != nil {
			state["turn"] = map[string]any{"id": turn.id, "status": turn.status}
		}
		return state, nil
	}
	if method == "model/list" {
		return e.rpc.Request(method, params)
	}
	if method == "thread/start" || method == "thread/resume" {
		e.mu.Lock()
		owned := e.id
		opening := e.opening
		e.mu.Unlock()
		if owned != "" || opening {
			// Reconnecting to this same task is allowed; creating a second task never is.
			if method == "thread/resume" && owned != "" && threadID == owned {
				return e.rpc.Request(method, params)
			}
			return nil, rejectExecutor("This executor already owns a task")
		}
		// Dormant resume needs admission by the durable pool before this executor starts.
		if method == "thread/resume" {
			return nil, rejectExecutor("Task has not been admitted to this executor")
		}
		e.mu.Lock()
		e.opening = true
		e.mu.Unlock()
		defer func() {
			e.mu.Lock()
			e.opening = false
			e.mu.Unlock()
		}()
		result, err := e.rpc.Request(method, params)
		if err != nil {
			return nil, err
		}
		created := executorThreadID(result)
		if !executorUUIDPattern.MatchString(created) {
			return nil, errors.New("Invalid native task identity")
		}
		e.onNativeCreated()
		e.mu.Lock()
		e.id = created
		e.mu.Unlock()
		if err := e.recordTask(created); err != nil {
			return nil, err
		}
		return result, nil
	}
	if !executorThreadMethods[method] {
		return nil, rejectExecutor("Operation is outside this native task scope")
	}
	e.mu.Lock()
	owned := e.id
	e.mu.Unlock()
	if owned == "" || threadID != owned {
		return nil, rejectExecutor("Operation is outside this native task scope")
	}
	result, err := e.rpc.Request(method, params)
	if err != nil {
		return nil, err
	}
	if method == "turn/start" {
		e.mu.Lock()
		e.materialized = true
		if turn := executorResultTurn(result); turn != nil && (e.turn == nil || e.turn.id != turn.id) {
			e.turn = turn
		}
		e.mu.Unlock()
	}
	if method == "turn/start" || method == "turn/steer" {
		e.mu.Lock()
		// A very short turn may complete before the response. Never replace its
		// terminal notification.
		if !e.settled {
			e.active = true
		}
		e.mu.Unlock()
	}
	if method == "thread/archive" {
		e.mu.Lock()
		e.active = false
		e.settled = true
		e.mu.Unlock()
	}
	return result, nil
}

func (e *TaskExecutor) handleRequest(packet map[string]any) {
	id, hasID := packet["id"]
	if !hasID {
		return
	}
	_ = e.rpc.RejectRequest(id, "Remote approval/input is unavailable")
}

func (e *TaskExecutor) handleNotification(packet map[string]any) {
	method, _ := packet["method"].(string)
	params, _ := nativejson.Fields(packet["params"])
	thread, _ := nativejson.Fields(params["thread"])
	id := executorText(params["threadId"])
	if id == "" {
		id = executorText(thread["id"])
	}
	e.mu.Lock()
	if id == "" || id != e.id {
		e.mu.Unlock()
		return
	}
	turnID := executorText(params["turnId"])
	if method == "turn/started" {
		e.active = true
		e.settled = false
		e.hasUser = false
		if turn := executorTurnFrom(params["turn"]); turn != nil {
			e.turn = turn
		}
	}
	if method == "turn/completed" {
		if turn := executorTurnFrom(params["turn"]); turn != nil {
			e.turn = turn
		}
	}
	if e.turn == nil && turnID != "" && (method == "item/started" || executorDeltaPattern.MatchString(method)) {
		e.turn = &executorTurn{id: turnID, status: "inProgress"}
	}
	if turnID != "" && e.turn != nil && turnID == e.turn.id {
		item, _ := nativejson.Fields(params["item"])
		if executorText(item["type"]) == "userMessage" {
			e.hasUser = true
			e.materialized = true
		}
	}
	if executorTerminalMethods[method] {
		e.active = false
		e.settled = true
	}
	e.mu.Unlock()
	for _, socket := range e.socketList() {
		e.write(socket, packet)
	}
	e.schedule()
}

// write sends one JSON packet under a write deadline, matching the TypeScript
// guard that drops a client whose payload or queue exceeds the payload bound.
func (e *TaskExecutor) write(socket *WebSocket, message any) {
	if socket.Closed() {
		return
	}
	text := executorEncode(message)
	if len(text) > executorMaxPayload {
		socket.Terminate()
		return
	}
	_ = socket.SetWriteDeadline(time.Now().Add(executorWriteTimeout))
	if err := socket.WriteText(text); err != nil {
		socket.Terminate()
		return
	}
	_ = socket.SetWriteDeadline(time.Time{})
}

func (e *TaskExecutor) schedule() {
	e.mu.Lock()
	e.stopTimerLocked()
	if e.retiring || e.pending > 0 || e.active {
		e.mu.Unlock()
		return
	}
	delay := e.idle
	if e.settled {
		delay = e.completed
	}
	e.timer = time.AfterFunc(delay, func() {
		if _, err := e.Retire(); err != nil {
			e.mu.Lock()
			onFailure := e.onFailure
			e.mu.Unlock()
			if onFailure != nil {
				onFailure(err)
			}
		}
	})
	e.mu.Unlock()
}

func (e *TaskExecutor) stopTimerLocked() {
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
}

// Retire fences admission before the native idle check; losing a client alone can
// never interrupt a turn.
func (e *TaskExecutor) Retire() (bool, error) {
	e.mu.Lock()
	if e.retiring || e.pending > 0 || e.active {
		e.mu.Unlock()
		return false, nil
	}
	e.retiring = true
	e.stopTimerLocked()
	e.mu.Unlock()
	if err := e.verifyIdle(); err != nil {
		e.retryRetirement()
		return false, err
	}
	if err := e.releaseNative(); err != nil {
		e.retryRetirement()
		return false, err
	}
	e.Disconnect()
	e.mu.Lock()
	onRetired := e.onRetired
	e.mu.Unlock()
	if onRetired != nil {
		onRetired()
	}
	return true, nil
}

func (e *TaskExecutor) retryRetirement() {
	e.mu.Lock()
	// Keep the native writer alive if its actual state cannot be verified.
	e.retiring = false
	e.mu.Unlock()
	e.mu.Lock()
	e.stopTimerLocked()
	e.timer = time.AfterFunc(e.idle, func() {
		if _, err := e.Retire(); err != nil {
			e.mu.Lock()
			onFailure := e.onFailure
			e.mu.Unlock()
			if onFailure != nil {
				onFailure(err)
			}
		}
	})
	e.mu.Unlock()
}

// verifyIdle confirms the native task is inactive before its writer is released.
func (e *TaskExecutor) verifyIdle() error {
	e.mu.Lock()
	id := e.id
	var turn *executorTurn
	if e.turn != nil {
		copied := *e.turn
		turn = &copied
	}
	e.mu.Unlock()
	if id == "" {
		return nil
	}
	result, err := e.rpc.Request("thread/read", map[string]any{"threadId": id, "includeTurns": false})
	if err != nil {
		return err
	}
	thread, _ := nativejson.Fields(executorField(result, "thread"))
	statusFields, _ := nativejson.Fields(thread["status"])
	status := executorText(statusFields["type"])
	inactive := status == "idle" || status == "notLoaded"
	if status == "systemError" {
		if turn != nil && executorTerminalTurnStatuses[turn.status] {
			inactive = true
		} else {
			turns, err := e.rpc.Request("thread/turns/list", map[string]any{
				"threadId": id, "limit": 1, "sortDirection": "desc", "itemsView": "notLoaded",
			})
			if err != nil {
				return err
			}
			data, _ := executorField(turns, "data").([]any)
			inactive = len(data) == 1 && executorTerminalTurnStatuses[executorTurnStatus(data[0])]
		}
	}
	if executorText(thread["id"]) != id || !inactive {
		return errors.New("Native task is not verified idle")
	}
	return nil
}

// Disconnect drops every client and this executor's endpoint, without touching
// the native writer.
func (e *TaskExecutor) Disconnect() {
	e.mu.Lock()
	e.stopTimerLocked()
	e.retiring = true
	server := e.server
	e.server = nil
	e.mu.Unlock()
	for _, socket := range e.socketList() {
		socket.Terminate()
	}
	if server != nil {
		_ = server.Close()
	}
}

func (e *TaskExecutor) trackSocket(socket *WebSocket, attached bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if attached {
		e.sockets[socket] = struct{}{}
		return
	}
	delete(e.sockets, socket)
}

func (e *TaskExecutor) socketList() []*WebSocket {
	e.mu.Lock()
	defer e.mu.Unlock()
	sockets := make([]*WebSocket, 0, len(e.sockets))
	for socket := range e.sockets {
		sockets = append(sockets, socket)
	}
	return sockets
}

// executorEncode mirrors JSON.stringify: no HTML escaping and no trailing
// newline, so native packet ordering survives the forwarding hop.
func executorEncode(value any) []byte {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return []byte("null")
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
}

func executorText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func executorField(value any, key string) any {
	fields, _ := nativejson.Fields(value)
	return fields[key]
}

// executorThreadID reads a created or resumed thread identity.
func executorThreadID(result any) string {
	thread, _ := nativejson.Fields(executorField(result, "thread"))
	return executorText(thread["id"])
}

// executorTurnFrom reads a complete turn object; a turn without both fields is
// not a status the executor may trust.
func executorTurnFrom(value any) *executorTurn {
	fields, ok := nativejson.Fields(value)
	if !ok {
		return nil
	}
	id := executorText(fields["id"])
	status := executorText(fields["status"])
	if id == "" || status == "" {
		return nil
	}
	return &executorTurn{id: id, status: status}
}

func executorResultTurn(result any) *executorTurn {
	return executorTurnFrom(executorField(result, "turn"))
}

func executorTurnStatus(value any) string {
	fields, ok := nativejson.Fields(value)
	if !ok {
		return ""
	}
	return executorText(fields["status"])
}
