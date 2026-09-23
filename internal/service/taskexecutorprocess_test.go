package service

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/codex"
)

const (
	// serviceHelperEnvironment selects the role of one helper process. The
	// service tests use this same test binary as the native app server and as the
	// lifetime monitor, so the real spawn paths are exercised without a second
	// artifact.
	serviceHelperEnvironment = "FILO_TEST_SERVICE_HELPER"
	// serviceHelperLogEnvironment names the file the native fixture appends every
	// received method name to.
	serviceHelperLogEnvironment = "FILO_TEST_SERVICE_HELPER_LOG"
)

// TestMain dispatches the helper roles before the test binary parses any flag.
func TestMain(m *testing.M) {
	if code, handled := runServiceHelperProcess(os.Args); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// runServiceHelperProcess runs one helper role. The second return value reports
// whether this process is a helper at all.
func runServiceHelperProcess(arguments []string) (int, bool) {
	recordServiceHelperConsoleState()
	switch os.Getenv(serviceHelperEnvironment) {
	case "console-exit":
		return 0, true
	case "guardian":
		return runGuardianHelper(arguments), true
	case "guardian-wait":
		return runGuardianWaitHelper(), true
	case "guardian-exit":
		return 3, true
	case "native":
		return runNativeHelper(arguments, false), true
	case "native-silent":
		return runNativeHelper(arguments, true), true
	}
	return 0, false
}

// runNativeHelper serves the native app server wire: the same subcommand and
// endpoint arguments the executor passes to a real desktop runtime. A silent run
// binds nothing, which is how an unreachable native child is exercised.
func runNativeHelper(arguments []string, silent bool) int {
	url, tokenPath := "", ""
	for index, argument := range arguments {
		if index+1 >= len(arguments) {
			break
		}
		switch argument {
		case "--listen":
			url = arguments[index+1]
		case "--ws-token-file":
			tokenPath = arguments[index+1]
		}
	}
	if url == "" || tokenPath == "" {
		return 2
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return 2
	}
	if silent {
		// An unreachable child stays alive without binding, so the keeper's
		// connection attempts fail until its startup budget expires.
		for {
			time.Sleep(time.Hour)
		}
	}
	listener, err := net.Listen("tcp", strings.TrimPrefix(url, "ws://"))
	if err != nil {
		return 2
	}
	log := &nativeHelperLog{path: os.Getenv(serviceHelperLogEnvironment)}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Origin") != "" ||
			request.Header.Get("Authorization") != "Bearer "+string(token) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		socket, err := codex.UpgradeWebSocket(writer, request)
		if err != nil {
			return
		}
		go serveNativeHelperSocket(socket, log)
	})}
	go func() { _ = server.Serve(listener) }()
	// The keeper ends this child by killing it after the transport closes: a
	// real desktop runtime does not consume stdin.
	for {
		time.Sleep(time.Hour)
	}
}

// serveNativeHelperSocket answers the native methods of one executor lifetime.
func serveNativeHelperSocket(socket *codex.WebSocket, log *nativeHelperLog) {
	for {
		message, err := socket.ReadMessage()
		if err != nil {
			socket.Terminate()
			return
		}
		var packet map[string]any
		if json.Unmarshal(message, &packet) != nil {
			continue
		}
		method, _ := packet["method"].(string)
		log.record(method)
		identifier, hasID := packet["id"]
		if !hasID {
			continue
		}
		_ = socket.WriteText(nativeHelperAnswer(method, identifier, packet["params"]))
	}
}

// nativeHelperAnswer is the native result of one method.
func nativeHelperAnswer(method string, identifier any, rawParams any) []byte {
	params, _ := rawParams.(map[string]any)
	threadID, _ := params["threadId"].(string)
	result := map[string]any{}
	switch method {
	case "thread/start":
		created, err := randomUUID()
		if err != nil {
			created = ""
		}
		result = map[string]any{"thread": map[string]any{
			"id":     created,
			"status": map[string]any{"type": "idle"},
		}}
	case "thread/read", "thread/resume":
		result = map[string]any{"thread": map[string]any{
			"id":     threadID,
			"status": map[string]any{"type": "idle"},
		}}
	case "thread/turns/list":
		result = map[string]any{"data": []any{}}
	}
	payload, err := json.Marshal(map[string]any{"id": identifier, "result": result})
	if err != nil {
		return nil
	}
	return payload
}

// nativeHelperLog appends every received method to one shared file.
type nativeHelperLog struct {
	path string
}

func (l *nativeHelperLog) record(method string) {
	if l.path == "" || method == "" {
		return
	}
	file, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = file.WriteString(method + "\n")
	_ = file.Close()
}

// nativeHelperMethods reads the recorded method sequence.
func nativeHelperMethods(t *testing.T, path string) []string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the native method log: %v", err)
	}
	var methods []string
	for _, line := range strings.Split(string(content), "\n") {
		if line != "" {
			methods = append(methods, line)
		}
	}
	return methods
}

// executorStateView is the test view of a durable executor record.
type executorStateView struct {
	Phase     string  `json:"phase"`
	KeeperPid int     `json:"keeperPid"`
	NativePid *int    `json:"nativePid"`
	TaskID    *string `json:"taskId"`
	URL       *string `json:"url"`
}

func readExecutorStateView(t *testing.T, directory string) (executorStateView, bool) {
	t.Helper()
	value, err := ReadExecutorFile(directory, executorStateName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return executorStateView{}, false
		}
		t.Fatalf("read the executor state: %v", err)
	}
	var view executorStateView
	if err := json.Unmarshal(Marshal(value), &view); err != nil {
		t.Fatalf("decode the executor state: %v", err)
	}
	return view, true
}

// waitForCondition polls until the condition holds or the budget expires.
func waitForCondition(t *testing.T, budget time.Duration, description string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

// awaitExecutorReadiness waits for one run to publish its private endpoint. A run
// that ends first is reported with its own error, because the run is bounded by
// its startup deadline and an anonymous timeout would hide why it aborted. A
// reserved loopback endpoint can still be taken between the reservation and the
// native bind, and that is exactly the abort this reports.
func awaitExecutorReadiness(t *testing.T, fixture *executorProcessFixture, done <-chan error) executorStateView {
	t.Helper()
	var view executorStateView
	var runErr error
	ended := false
	waitForCondition(t, 30*time.Second, "the published readiness", func() bool {
		select {
		case runErr = <-done:
			ended = true
			return true
		default:
		}
		state, exists := readExecutorStateView(t, fixture.directory)
		if !exists || state.Phase != "ready" || state.URL == nil {
			return false
		}
		view = state
		return true
	})
	if ended {
		t.Fatalf("the executor ended before publishing readiness: %v", runErr)
	}
	return view
}

// executorProcessFixture is one prepared private executor directory.
type executorProcessFixture struct {
	t         *testing.T
	private   string
	directory string
	workspace string
	log       string
	config    ExecutorConfig

	mu       sync.Mutex
	bindings []string
	native   []int
}

func newExecutorProcessFixture(t *testing.T) *executorProcessFixture {
	t.Helper()
	// The creation provenance is the directory layout itself, so the fixture is
	// one real `<private>/executors/<id>` directory.
	standalone := t.TempDir()
	private := filepath.Join(standalone, serviceTaskDirectoryName)
	id, err := randomUUID()
	if err != nil {
		t.Fatalf("randomUUID: %v", err)
	}
	directory := filepath.Join(private, "executors", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create the executor directory: %v", err)
	}
	fixture := &executorProcessFixture{
		t:         t,
		private:   private,
		directory: directory,
		workspace: t.TempDir(),
		log:       filepath.Join(t.TempDir(), "native-methods.log"),
	}
	token, err := executorSecret()
	if err != nil {
		t.Fatalf("executorSecret: %v", err)
	}
	nativeToken, err := executorSecret()
	if err != nil {
		t.Fatalf("executorSecret: %v", err)
	}
	fixture.config = ExecutorConfig{Token: token, NativeToken: nativeToken, Workspace: fixture.workspace}
	t.Cleanup(fixture.killNatives)
	return fixture
}

func (f *executorProcessFixture) writeConfig(taskID *string) {
	f.t.Helper()
	config := f.config
	config.TaskID = taskID
	if err := SaveExecutorFile(f.directory, executorConfigName, config); err != nil {
		f.t.Fatalf("write the executor configuration: %v", err)
	}
}

// guardian is the lifetime port of one run: alive without a real monitor, and
// every binding recorded for the test.
func (f *executorProcessFixture) guardian() *NativeGuardian {
	return &NativeGuardian{
		PID:   os.Getpid(),
		Alive: func() bool { return true },
		Ready: func() error { return nil },
		Bind: func(taskID string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.bindings = append(f.bindings, taskID)
			return nil
		},
	}
}

// boundTasks is the recorded binding sequence.
func (f *executorProcessFixture) boundTasks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bindings...)
}

// trackNative records one spawned native child so a failing test still cleans it
// up.
func (f *executorProcessFixture) trackNative(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.native = append(f.native, pid)
}

// containsMethod reports whether one method was observed on the native wire.
func containsMethod(methods []string, method string) bool {
	for _, observed := range methods {
		if observed == method {
			return true
		}
	}
	return false
}

// options is one run over this fixture, with the native fixture as the desktop
// runtime and every budget shortened for the test.
func (f *executorProcessFixture) options(idle time.Duration) ExecutorProcessOptions {
	f.t.Setenv(serviceHelperEnvironment, "native")
	f.t.Setenv(serviceHelperLogEnvironment, f.log)
	return ExecutorProcessOptions{
		Directory:       f.directory,
		DesktopRuntime:  func() (string, error) { return os.Executable() },
		Guardian:        func(string) (*NativeGuardian, error) { return f.guardian(), nil },
		Idle:            idle,
		StartupDeadline: 10 * time.Second,
		ReleaseGrace:    500 * time.Millisecond,
		ReconnectDelay:  100 * time.Millisecond,
	}
}

func (f *executorProcessFixture) killNatives() {
	f.mu.Lock()
	natives := append([]int(nil), f.native...)
	f.mu.Unlock()
	for _, pid := range natives {
		terminateProcess(pid)
		waitForProcessExit(f.t, pid, "the fixture native child")
	}
}

// TestRunTaskExecutorRefusesARelativeDirectory pins the first gate of the run.
func TestRunTaskExecutorRefusesARelativeDirectory(t *testing.T) {
	err := RunTaskExecutorWith(ExecutorProcessOptions{
		Directory:      filepath.Join("relative", "executor"),
		DesktopRuntime: func() (string, error) { return "", errors.New("unused") },
	})
	if err == nil || err.Error() != "Executor directory must be absolute" {
		t.Fatalf("a relative executor directory = %v", err)
	}
}

// TestRunTaskExecutorRefusesAStaleStartedClaim pins exclusive admission: a
// record left by an interrupted attempt is investigated, never overwritten, and
// no native process may start for it.
func TestRunTaskExecutorRefusesAStaleStartedClaim(t *testing.T) {
	f := newExecutorProcessFixture(t)
	f.writeConfig(nil)
	if err := os.WriteFile(filepath.Join(f.directory, executorClaimName), []byte("4096"), 0o600); err != nil {
		t.Fatalf("prepare the stale claim: %v", err)
	}
	launched := false
	options := f.options(time.Minute)
	options.DesktopRuntime = func() (string, error) {
		launched = true
		return "", errors.New("unused")
	}
	err := RunTaskExecutorWith(options)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("running over a stale claim = %v", err)
	}
	if launched {
		t.Fatal("a stale claim still reached the desktop runtime")
	}
	if _, exists := readExecutorStateView(t, f.directory); exists {
		t.Fatal("a stale claim still published a state record")
	}
	claim, err := os.ReadFile(filepath.Join(f.directory, executorClaimName))
	if err != nil {
		t.Fatalf("read the claim: %v", err)
	}
	if string(claim) != "4096" {
		t.Fatalf("the stale claim = %q", claim)
	}
}

// TestRunTaskExecutorBindsRetiresAndReleasesItsNativeTask runs the complete
// lifecycle: a native child is started on a reserved endpoint, a created task is
// recorded for the guardian and the durable pool, and an idle executor releases
// exactly its own child.
func TestRunTaskExecutorBindsRetiresAndReleasesItsNativeTask(t *testing.T) {
	f := newExecutorProcessFixture(t)
	f.config.CreateTrace = strings.Repeat("c", 32)
	f.writeConfig(nil)
	options := f.options(1500 * time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- RunTaskExecutorWith(options) }()
	view := awaitExecutorReadiness(t, f, done)
	if view.NativePid == nil || !processAlive(*view.NativePid) {
		t.Fatalf("the ready record names native pid %v", view.NativePid)
	}
	f.trackNative(*view.NativePid)
	if view.KeeperPid != os.Getpid() {
		t.Fatalf("the ready record keeper = %d", view.KeeperPid)
	}
	claim, err := os.ReadFile(filepath.Join(f.directory, executorClaimName))
	if err != nil || string(claim) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("the admission claim = %q, %v", claim, err)
	}
	connection, err := codex.ConnectCodexHost(context.Background(), *view.URL,
		codex.HostOptions{Token: &f.config.Token})
	if err != nil {
		t.Fatalf("join the private executor: %v", err)
	}
	created, err := connection.Rpc.Request("thread/start", map[string]any{})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	id := factoryCreatedID(t, created)
	origin, err := ReadExecutorFile(filepath.Join(f.private, "tasks", id), executorOriginName)
	if err != nil {
		t.Fatalf("read the creation provenance: %v", err)
	}
	if reference, err := ExecutorReference(origin); err != nil || reference != filepath.Base(f.directory) {
		t.Fatalf("the creation provenance = %q, %v", reference, err)
	}
	if bindings := f.boundTasks(); len(bindings) != 1 || bindings[0] != id {
		t.Fatalf("the guardian bindings = %v", bindings)
	}
	events := readCreateDiagnosticEvents(t, filepath.Join(filepath.Dir(f.private), createHelperLogName))
	wantStages := []string{
		createStageExecutorNativeAcknowledged,
		createStageExecutorProvenanceRecorded,
		createStageExecutorGuardianBound,
	}
	if stages := createDiagnosticStagesOf(events); !reflect.DeepEqual(stages, wantStages) {
		t.Fatalf("the executor creation stages = %v, want %v", stages, wantStages)
	}
	var lifecycle error
	waitForCondition(t, 20*time.Second, "the retired lifecycle", func() bool {
		select {
		case lifecycle = <-done:
			return true
		default:
			return false
		}
	})
	if lifecycle != nil {
		t.Fatalf("the executor lifecycle: %v", lifecycle)
	}
	final, exists := readExecutorStateView(t, f.directory)
	if !exists || final.Phase != "retired" || final.URL != nil {
		t.Fatalf("the final state = %+v", final)
	}
	if final.TaskID == nil || *final.TaskID != id {
		t.Fatalf("the final task = %v", final.TaskID)
	}
	waitForCondition(t, 10*time.Second, "the released native child", func() bool {
		return !processAlive(*view.NativePid)
	})
	connection.Close()
	if methods := nativeHelperMethods(t, f.log); !containsMethod(methods, "thread/read") {
		t.Fatalf("the native methods = %v", methods)
	}
}

// TestRunTaskExecutorReleasesAnUnreachableNativeChild pins the startup budget: a
// child that never publishes an endpoint is released instead of being retried
// forever, and no task is ever admitted to it.
func TestRunTaskExecutorReleasesAnUnreachableNativeChild(t *testing.T) {
	f := newExecutorProcessFixture(t)
	f.writeConfig(nil)
	options := f.options(time.Minute)
	options.StartupDeadline = 800 * time.Millisecond
	options.ReconnectDelay = 100 * time.Millisecond
	options.ReleaseGrace = 300 * time.Millisecond
	f.t.Setenv(serviceHelperEnvironment, "native-silent")
	err := RunTaskExecutorWith(options)
	if err == nil || !strings.Contains(err.Error(), "Codex connection failed") {
		t.Fatalf("an unreachable native child = %v", err)
	}
	view, exists := readExecutorStateView(t, f.directory)
	if !exists || view.NativePid == nil {
		t.Fatalf("the abandoned record = %+v", view)
	}
	// The abandoned record stays for investigation: the run never published
	// readiness, so no client could have admitted a task to the released child.
	// Each failed reconnect already marked the record disconnected, which is
	// the durable state an operator finds after this abort.
	if view.Phase != "disconnected" {
		t.Fatalf("the abandoned phase = %q", view.Phase)
	}
	// The released child is never a pid the run still owns, so it is observed
	// ending rather than assumed gone: a terminated process object can outlive
	// its termination by a short moment.
	if processAlive(*view.NativePid) {
		waitForProcessExit(t, *view.NativePid, "the released native child")
	}
	if bindings := f.boundTasks(); len(bindings) != 0 {
		t.Fatalf("an unreachable child still bound %v", bindings)
	}
	if _, err := os.Stat(f.log); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an unreachable child still reached the native wire: %v", err)
	}
}

// TestRunTaskExecutorNeverReplaysOntoALostNativeWriter kills the native child of
// a published executor: the keeper ends instead of replaying the accepted task
// onto a new or different writer.
func TestRunTaskExecutorNeverReplaysOntoALostNativeWriter(t *testing.T) {
	f := newExecutorProcessFixture(t)
	f.writeConfig(nil)
	options := f.options(time.Hour)
	done := make(chan error, 1)
	go func() { done <- RunTaskExecutorWith(options) }()
	view := awaitExecutorReadiness(t, f, done)
	f.trackNative(*view.NativePid)
	connection, err := codex.ConnectCodexHost(context.Background(), *view.URL,
		codex.HostOptions{Token: &f.config.Token})
	if err != nil {
		t.Fatalf("join the private executor: %v", err)
	}
	created, err := connection.Rpc.Request("thread/start", map[string]any{})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	id := factoryCreatedID(t, created)
	connection.Close()
	terminateProcess(*view.NativePid)
	waitForProcessExit(t, *view.NativePid, "the killed native child")
	if err := <-done; err != nil {
		t.Fatalf("the executor lifecycle: %v", err)
	}
	final, exists := readExecutorStateView(t, f.directory)
	if !exists || final.Phase != "exited" || final.URL != nil {
		t.Fatalf("the final state = %+v", final)
	}
	if final.TaskID == nil || *final.TaskID != id {
		t.Fatalf("the accepted task = %v", final.TaskID)
	}
	// Reconnecting to the same authenticated host is allowed, but a mutation is
	// never replayed: no resume may appear for a writer that is already gone.
	if methods := nativeHelperMethods(t, f.log); containsMethod(methods, "thread/resume") {
		t.Fatalf("the native methods = %v", methods)
	}
}

// TestReserveExecutorURLReservesALoopbackEndpoint pins the reserved endpoint
// form and proves it is released again for the native child.
func TestReserveExecutorURLReservesALoopbackEndpoint(t *testing.T) {
	url, err := reserveExecutorURL()
	if err != nil {
		t.Fatalf("reserveExecutorURL: %v", err)
	}
	if !endpointPattern.MatchString(url) {
		t.Fatalf("the reserved endpoint = %q", url)
	}
	listener, err := net.Listen("tcp", strings.TrimPrefix(url, "ws://"))
	if err != nil {
		t.Fatalf("the reserved endpoint is still held: %v", err)
	}
	_ = listener.Close()
}

// TestNativeProcessEnvironmentLeadsPathAndDropsAppServerOverrides pins the child
// environment: no CODEX_APP_SERVER_ override survives, and the native runtime
// directory leads PATH.
func TestNativeProcessEnvironmentLeadsPathAndDropsAppServerOverrides(t *testing.T) {
	t.Setenv("CODEX_APP_SERVER_HOME", "C:\\elsewhere")
	t.Setenv("Codex_App_Server_Token", "leaked")
	environment := nativeProcessEnvironment(`C:\native\codex.exe`)
	paths, overrides := 0, 0
	leading := ""
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if strings.EqualFold(key, "PATH") {
			paths++
			leading = value
			continue
		}
		if strings.HasPrefix(strings.ToUpper(key), "CODEX_APP_SERVER_") {
			overrides++
		}
	}
	if paths != 1 {
		t.Fatalf("the child PATH entries = %d", paths)
	}
	if !strings.HasPrefix(leading, `C:\native;`) {
		t.Fatalf("the child PATH = %q", leading)
	}
	if overrides != 0 {
		t.Fatalf("CODEX_APP_SERVER_ overrides reaching the child = %d", overrides)
	}
}

// TestParseDesktopRuntime pins the resolver shape gate.
func TestParseDesktopRuntime(t *testing.T) {
	usable, err := parseDesktopRuntime(`{"Executable":"C:\\Users\\sample\\codex.exe"}`)
	if err != nil || usable != `C:\Users\sample\codex.exe` {
		t.Fatalf("an absolute runtime = %q, %v", usable, err)
	}
	for _, output := range []string{`{}`, `{"Executable":""}`, `{"Executable":"codex.exe"}`} {
		if _, err := parseDesktopRuntime(output); err == nil ||
			err.Error() != "Desktop runtime is unavailable" {
			t.Fatalf("resolver output %s = %v", output, err)
		}
	}
	if _, err := parseDesktopRuntime("not json"); err == nil {
		t.Fatal("malformed resolver output was accepted")
	}
}
