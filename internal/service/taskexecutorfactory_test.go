package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/nativejson"
)

// factoryNative stands in for the native host behind one fixture executor: it
// answers the wire methods the factory and its executed tasks rely on.
type factoryNative struct {
	rpc     *codex.Rpc
	inbound *io.PipeReader
	toRPC   *io.PipeWriter
	fixture *executorFactoryFixture
}

func newFactoryNative(f *executorFactoryFixture) *factoryNative {
	rpcInput, nativeOutput := io.Pipe()
	nativeInput, rpcOutput := io.Pipe()
	native := &factoryNative{
		rpc:     codex.NewRpc(rpcInput, rpcOutput),
		inbound: nativeInput,
		toRPC:   nativeOutput,
		fixture: f,
	}
	go native.serve()
	return native
}

func (n *factoryNative) serve() {
	scanner := bufio.NewScanner(n.inbound)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var packet map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &packet); err != nil {
			continue
		}
		method, _ := packet["method"].(string)
		identifier, isRequest := packet["id"]
		if method == "" || !isRequest {
			continue
		}
		n.answer(method, identifier, packet["params"])
	}
}

func (n *factoryNative) answer(method string, identifier any, rawParams any) {
	params, _ := rawParams.(map[string]any)
	n.fixture.recordInput(method)
	result := map[string]any{}
	switch method {
	case "thread/start":
		created, err := randomUUID()
		if err != nil {
			return
		}
		result = map[string]any{"thread": map[string]any{"id": created}}
	case "thread/read", "thread/resume":
		threadID, _ := params["threadId"].(string)
		result = map[string]any{"thread": map[string]any{
			"id":     threadID,
			"status": map[string]any{"type": "idle"},
		}}
	}
	payload, err := json.Marshal(map[string]any{"id": identifier, "result": result})
	if err != nil {
		return
	}
	_, _ = n.toRPC.Write(append(payload, '\n'))
}

// factoryExecutor is one launched native executor with its published state.
type factoryExecutor struct {
	core      *codex.TaskExecutor
	directory string
	state     ExecutorState
}

type executorFactoryFixture struct {
	t         *testing.T
	directory string

	mu      sync.Mutex
	inputs  []string
	configs []ExecutorConfig
	running []*factoryExecutor
	natives []*factoryNative
}

func newExecutorFactoryFixture(t *testing.T) *executorFactoryFixture {
	t.Helper()
	fixture := &executorFactoryFixture{t: t, directory: t.TempDir()}
	t.Cleanup(fixture.close)
	return fixture
}

// start is the launcher the factory under test calls. It publishes the same
// durable identity a real runner would: a ready endpoint plus live pids.
func (f *executorFactoryFixture) start(directory string) error {
	value, err := ReadExecutorFile(directory, executorConfigName)
	if err != nil {
		return err
	}
	config, err := ParseExecutorConfig(value)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.configs = append(f.configs, config)
	f.mu.Unlock()
	keeperPid := os.Getpid()
	entry := &factoryExecutor{directory: directory, state: ExecutorState{
		Phase:     "starting",
		KeeperPid: keeperPid,
		NativePid: &keeperPid,
		NativeURL: factoryTextPointer("ws://127.0.0.1:12345"),
		TaskID:    config.TaskID,
	}}
	admitted := ""
	if config.TaskID != nil {
		admitted = *config.TaskID
	}
	native := newFactoryNative(f)
	core, err := codex.NewTaskExecutor(native.rpc, codex.TaskExecutorOptions{
		Token:      config.Token,
		AdmittedID: admitted,
		ReleaseNative: func() error {
			f.mutate(entry, func(state *ExecutorState) {
				state.Phase = "retired"
				state.URL = nil
			})
			return SaveExecutorState(directory, f.snapshot(entry))
		},
		RecordTask: func(id string) error {
			f.mutate(entry, func(state *ExecutorState) { state.TaskID = factoryTextPointer(id) })
			if err := SaveExecutorState(directory, f.snapshot(entry)); err != nil {
				return err
			}
			return RecordCreatedTask(directory, id)
		},
		Idle:      60 * time.Second,
		Completed: 500 * time.Millisecond,
	})
	if err != nil {
		return err
	}
	entry.core = core
	url, err := core.Listen()
	if err != nil {
		return err
	}
	f.mutate(entry, func(state *ExecutorState) {
		state.Phase = "ready"
		state.URL = factoryTextPointer(url)
	})
	if err := SaveExecutorState(directory, f.snapshot(entry)); err != nil {
		return err
	}
	f.mu.Lock()
	f.running = append(f.running, entry)
	f.natives = append(f.natives, native)
	f.mu.Unlock()
	return nil
}

func (f *executorFactoryFixture) close() {
	f.mu.Lock()
	running := append([]*factoryExecutor(nil), f.running...)
	natives := append([]*factoryNative(nil), f.natives...)
	f.mu.Unlock()
	for _, entry := range running {
		if entry.core != nil {
			_, _ = entry.core.Retire()
		}
	}
	for _, native := range natives {
		native.rpc.Close()
	}
}

func (f *executorFactoryFixture) recordInput(method string) {
	f.mu.Lock()
	f.inputs = append(f.inputs, method)
	f.mu.Unlock()
}

func (f *executorFactoryFixture) inputList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inputs...)
}

func (f *executorFactoryFixture) configList() []ExecutorConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ExecutorConfig(nil), f.configs...)
}

func (f *executorFactoryFixture) runningCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.running)
}

func (f *executorFactoryFixture) runningExecutor(index int) *factoryExecutor {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index >= len(f.running) {
		f.t.Fatalf("the fixture launched only %d executors", len(f.running))
	}
	return f.running[index]
}

func (f *executorFactoryFixture) mutate(entry *factoryExecutor, change func(*ExecutorState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(&entry.state)
}

func (f *executorFactoryFixture) snapshot(entry *factoryExecutor) ExecutorState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return entry.state
}

// factoryFor builds one factory over the fixture with a matching workspace and
// the caller's capacity and readiness.
func factoryFor(t *testing.T, f *executorFactoryFixture, capacity int, readiness time.Duration) *TaskExecutorFactory {
	t.Helper()
	factory, err := NewTaskExecutorFactory(TaskExecutorFactoryOptions{
		Directory: f.directory,
		Workspace: filepath.Dir(f.directory),
		Launch:    f.start,
		Capacity:  capacity,
		Readiness: readiness,
	})
	if err != nil {
		t.Fatalf("NewTaskExecutorFactory: %v", err)
	}
	return factory
}

func factoryNested(value any, key string) any {
	fields, _ := nativejson.Fields(value)
	return fields[key]
}

func factoryCreatedID(t *testing.T, value any) string {
	t.Helper()
	id, _ := factoryNested(factoryNested(value, "thread"), "id").(string)
	if !uuidPattern.MatchString(id) {
		t.Fatalf("the created native task identity = %q", id)
	}
	return id
}

func factoryTextPointer(value string) *string { return &value }

func TestTaskExecutorFactoryReconnectsTheSameDurableCreation(t *testing.T) {
	f := newExecutorFactoryFixture(t)
	factory := factoryFor(t, f, 0, 0)
	noise, err := randomUUID()
	if err != nil {
		t.Fatalf("randomUUID: %v", err)
	}
	// A directory without configuration or a started claim must not poison
	// creation, which is what an interrupted first attempt leaves behind.
	if err := os.MkdirAll(filepath.Join(f.directory, "executors", noise), 0o700); err != nil {
		t.Fatalf("prepare a partial executor directory: %v", err)
	}
	trace := strings.Repeat("a", 32)
	first, err := factory.Open(withCreateTrace(context.Background(), trace, nil), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if configs := f.configList(); len(configs) != 1 || configs[0].CreateTrace != trace {
		t.Fatalf("new executor configurations = %+v", configs)
	}
	created, err := first.Rpc.Request("thread/start", map[string]any{})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	id := factoryCreatedID(t, created)
	if owned, err := factory.Owns(id); err != nil || !owned {
		t.Fatalf("owns(%s) = %v, %v", id, owned, err)
	}
	first.Close()
	factory.Close()
	replacement := factoryFor(t, f, 0, 0)
	var (
		one, two   *codex.CodexHost
		err1, err2 error
		wait       sync.WaitGroup
	)
	wait.Add(2)
	go func() {
		defer wait.Done()
		one, err1 = replacement.Open(context.Background(), &id)
	}()
	go func() {
		defer wait.Done()
		two, err2 = replacement.Open(context.Background(), &id)
	}()
	wait.Wait()
	if err1 != nil || err2 != nil {
		t.Fatalf("the replacement opens = %v, %v", err1, err2)
	}
	if one != two {
		t.Fatal("concurrent opens for one task returned two connections")
	}
	if count := f.runningCount(); count != 1 {
		t.Fatalf("the factory launched %d native executors", count)
	}
	if inputs := f.inputList(); !reflect.DeepEqual(inputs, []string{"thread/start"}) {
		t.Fatalf("native inputs = %v", inputs)
	}
	one.Close()
	replacement.Close()
}

func TestTaskExecutorFactoryAdmitsOnlyAnEndedFiloCreatedTask(t *testing.T) {
	f := newExecutorFactoryFixture(t)
	factory := factoryFor(t, f, 0, 0)
	unknown, err := randomUUID()
	if err != nil {
		t.Fatalf("randomUUID: %v", err)
	}
	if owned, err := factory.Owns(unknown); err != nil || owned {
		t.Fatalf("owns(unknown) = %v, %v", owned, err)
	}
	if _, err := factory.Open(context.Background(), &unknown); err == nil ||
		!strings.Contains(err.Error(), "outside Filo creation scope") {
		t.Fatalf("opening an ordinary task = %v", err)
	}
	if count := f.runningCount(); count != 0 {
		t.Fatalf("opening an ordinary task launched %d executors", count)
	}
	first, err := factory.Open(context.Background(), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	created, err := first.Rpc.Request("thread/start", map[string]any{})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	id := factoryCreatedID(t, created)
	first.Close()
	if retired, err := f.runningExecutor(0).core.Retire(); !retired || err != nil {
		t.Fatalf("retiring the first executor = %v, %v", retired, err)
	}
	if conn, err := factory.Follow(context.Background(), id); conn != nil || err != nil {
		t.Fatalf("following an ended task = %v, %v", conn, err)
	}
	if count := f.runningCount(); count != 1 {
		t.Fatalf("browsing an ended task launched %d executors", count)
	}
	resumed, err := factory.Open(withCreateTrace(context.Background(), strings.Repeat("b", 32), nil), &id)
	if err != nil {
		t.Fatalf("resuming the ended task: %v", err)
	}
	if _, err := resumed.Rpc.Request("thread/resume", map[string]any{"threadId": id}); err != nil {
		t.Fatalf("thread/resume: %v", err)
	}
	if count := f.runningCount(); count != 2 {
		t.Fatalf("resuming an ended task launched %d executors", count)
	}
	if configs := f.configList(); len(configs) != 2 || configs[1].CreateTrace != "" {
		t.Fatalf("replacement executor configurations = %+v", configs)
	}
	if state := f.snapshot(f.runningExecutor(1)); state.TaskID == nil || *state.TaskID != id {
		t.Fatalf("the replacement executor task = %v", state.TaskID)
	}
	if inputs := f.inputList(); !reflect.DeepEqual(inputs, []string{"thread/start", "thread/read", "thread/resume"}) {
		t.Fatalf("native inputs = %v", inputs)
	}
	resumed.Close()
	factory.Close()
}

func TestTaskExecutorFactoryBoundsCapacityAndNeverFallsThrough(t *testing.T) {
	f := newExecutorFactoryFixture(t)
	factory := factoryFor(t, f, 1, 200*time.Millisecond)
	connections := make([]*codex.CodexHost, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			connections[index], errs[index] = factory.Open(context.Background(), nil)
		}(index)
	}
	wait.Wait()
	fulfilled := 0
	var connection *codex.CodexHost
	for index, err := range errs {
		if err == nil {
			fulfilled++
			connection = connections[index]
		}
	}
	if fulfilled != 1 {
		t.Fatalf("fulfilled opens = %d (%v, %v)", fulfilled, errs[0], errs[1])
	}
	created, err := connection.Rpc.Request("thread/start", map[string]any{})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	id := factoryCreatedID(t, created)
	connection.Close()
	entry := f.runningExecutor(0)
	f.mutate(entry, func(state *ExecutorState) {
		state.Phase = "disconnected"
		state.URL = nil
	})
	if err := SaveExecutorState(entry.directory, f.snapshot(entry)); err != nil {
		t.Fatalf("publish the disconnected state: %v", err)
	}
	if _, err := factory.Open(context.Background(), &id); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("opening a disconnected executor = %v", err)
	}
	if _, err := factory.Follow(context.Background(), id); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("following a disconnected executor = %v", err)
	}
	if count := f.runningCount(); count != 1 {
		t.Fatalf("the factory launched %d native executors", count)
	}
	if inputs := f.inputList(); !reflect.DeepEqual(inputs, []string{"thread/start"}) {
		t.Fatalf("native inputs = %v", inputs)
	}
	factory.Close()
	if _, err := factory.Open(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "closing") {
		t.Fatalf("opening after close = %v", err)
	}
}

func TestTaskExecutorFactoryFollowObservesRetirementWhileConnecting(t *testing.T) {
	f := newExecutorFactoryFixture(t)
	factory := factoryFor(t, f, 0, 0)
	first, err := factory.Open(context.Background(), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	created, err := first.Rpc.Request("thread/start", map[string]any{})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	id := factoryCreatedID(t, created)
	first.Close()
	entry := f.runningExecutor(0)
	if retired, err := entry.core.Retire(); !retired || err != nil {
		t.Fatalf("retiring the first executor = %v, %v", retired, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("the listener bound %v", listener.Addr())
	}
	var connections atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		socket, err := codex.UpgradeWebSocket(writer, request)
		if err != nil {
			return
		}
		// The executor retires itself exactly while a follower is connecting.
		connections.Add(1)
		f.mutate(entry, func(state *ExecutorState) {
			state.Phase = "retired"
			state.URL = nil
		})
		_ = SaveExecutorState(entry.directory, f.snapshot(entry))
		socket.CloseWith(1000, "closed")
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	f.mutate(entry, func(state *ExecutorState) {
		state.Phase = "ready"
		state.URL = factoryTextPointer(fmt.Sprintf("ws://127.0.0.1:%d", address.Port))
	})
	if err := SaveExecutorState(entry.directory, f.snapshot(entry)); err != nil {
		t.Fatalf("publish the ready state: %v", err)
	}
	conn, err := factory.Follow(context.Background(), id)
	if conn != nil || err != nil {
		t.Fatalf("following a retiring executor = %v, %v", conn, err)
	}
	if count := connections.Load(); count != 1 {
		t.Fatalf("the follower connected %d times", count)
	}
	if count := f.runningCount(); count != 1 {
		t.Fatalf("following launched %d executors", count)
	}
	var observed []string
	for _, method := range f.inputList() {
		if method != "thread/read" {
			observed = append(observed, method)
		}
	}
	if !reflect.DeepEqual(observed, []string{"thread/start"}) {
		t.Fatalf("native inputs outside the retirement check = %v", observed)
	}
	factory.Close()
}
