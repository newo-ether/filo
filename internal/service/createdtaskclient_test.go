package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

// taskClientCall records one private call a created task transport made.
type taskClientCall struct {
	path  string
	input any
}

// taskStreamScript is one scripted task event response: either a fixed body or a
// body the fixture keeps open until the test ends.
type taskStreamScript struct {
	status int
	body   string
	reader io.ReadCloser
	err    error
}

// taskClientFixture serves one private transport over recorded calls and
// scripted streams.
type taskClientFixture struct {
	t       *testing.T
	private string
	mu      sync.Mutex
	calls   []taskClientCall
	answer  func(path string, input any) (json.RawMessage, error)
	streams []*taskStreamScript
	opens   []string
	changes []string
	closers []io.Closer
	client  *CreatedTaskClient
}

func newTaskClientFixture(t *testing.T) *taskClientFixture {
	t.Helper()
	private := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatalf("create private directory: %v", err)
	}
	fixture := &taskClientFixture{t: t, private: private}
	fixture.client = NewCreatedTaskClient(private, fixture.call, fixture.open, fixture.changed)
	t.Cleanup(func() {
		fixture.client.Close()
		fixture.closeStreams()
	})
	return fixture
}

func (f *taskClientFixture) call(_ context.Context, path string, input any) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, taskClientCall{path: path, input: input})
	answer := f.answer
	f.mu.Unlock()
	if answer == nil {
		return nil, errors.New("unexpected private call: " + path)
	}
	return answer(path, input)
}

func (f *taskClientFixture) open(_ context.Context, path string) (*http.Response, error) {
	f.mu.Lock()
	f.opens = append(f.opens, path)
	var script *taskStreamScript
	if len(f.streams) > 0 {
		script, f.streams = f.streams[0], f.streams[1:]
	}
	f.mu.Unlock()
	if script == nil {
		return nil, errors.New("no scripted task stream for " + path)
	}
	if script.err != nil {
		return nil, script.err
	}
	status := script.status
	if status == 0 {
		status = http.StatusOK
	}
	body := script.reader
	if body == nil {
		body = io.NopCloser(strings.NewReader(script.body))
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{taskEventStreamType}},
		Body:       body,
	}, nil
}

func (f *taskClientFixture) changed(id string) {
	f.mu.Lock()
	f.changes = append(f.changes, id)
	f.mu.Unlock()
}

func (f *taskClientFixture) snapshotCalls() []taskClientCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]taskClientCall(nil), f.calls...)
}

func (f *taskClientFixture) openList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opens...)
}

func (f *taskClientFixture) changeList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.changes...)
}

// scriptStream queues one complete stream body.
func (f *taskClientFixture) scriptStream(body string) {
	f.mu.Lock()
	f.streams = append(f.streams, &taskStreamScript{body: body})
	f.mu.Unlock()
}

// scriptStatus queues one refused stream response.
func (f *taskClientFixture) scriptStatus(status int) {
	f.mu.Lock()
	f.streams = append(f.streams, &taskStreamScript{status: status})
	f.mu.Unlock()
}

// blockStream queues one stream whose body stays open until the test ends, so a
// subscription occupies its slot for the whole test.
func (f *taskClientFixture) blockStream() {
	reader, writer := io.Pipe()
	f.mu.Lock()
	f.streams = append(f.streams, &taskStreamScript{reader: reader})
	f.closers = append(f.closers, reader, writer)
	f.mu.Unlock()
}

// openScriptedStream queues one open stream and returns the writer of it. The
// write runs off the test goroutine because a pipe write blocks until the reader
// consumes it.
func (f *taskClientFixture) openScriptedStream() func(string) {
	reader, writer := io.Pipe()
	f.mu.Lock()
	f.streams = append(f.streams, &taskStreamScript{reader: reader})
	f.closers = append(f.closers, reader, writer)
	f.mu.Unlock()
	return func(payload string) {
		go func() { _, _ = io.WriteString(writer, payload) }()
	}
}

func (f *taskClientFixture) closeStreams() {
	f.mu.Lock()
	closers := f.closers
	f.closers = nil
	f.mu.Unlock()
	for _, closer := range closers {
		_ = closer.Close()
	}
}

// createTask records real creation provenance and returns one admitted task id.
func (f *taskClientFixture) createTask() string {
	f.t.Helper()
	executor, err := randomUUID()
	if err != nil {
		f.t.Fatalf("randomUUID: %v", err)
	}
	directory := filepath.Join(f.private, "executors", executor)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		f.t.Fatalf("create executor directory: %v", err)
	}
	return f.recordProvenance(directory)
}

// recordProvenance records the creation provenance of one new task.
func (f *taskClientFixture) recordProvenance(directory string) string {
	f.t.Helper()
	task, err := randomUUID()
	if err != nil {
		f.t.Fatalf("randomUUID: %v", err)
	}
	if err := RecordCreatedTask(directory, task); err != nil {
		f.t.Fatalf("RecordCreatedTask: %v", err)
	}
	return task
}

// createTaskDirectory creates one private task directory without any creation
// provenance, which is exactly what a desktop-created session looks like here.
func (f *taskClientFixture) createTaskDirectory() string {
	f.t.Helper()
	task, err := randomUUID()
	if err != nil {
		f.t.Fatalf("randomUUID: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(f.private, "tasks", task), 0o700); err != nil {
		f.t.Fatalf("create task directory: %v", err)
	}
	return task
}

func (f *taskClientFixture) subscriptionCount() int {
	f.client.mu.Lock()
	defer f.client.mu.Unlock()
	return len(f.client.subscriptions)
}

// subscriptionError reads one live subscription's terminal failure under the
// lock of the transport.
func (f *taskClientFixture) subscriptionError(id string) error {
	f.client.mu.Lock()
	defer f.client.mu.Unlock()
	if subscription := f.client.subscriptions[id]; subscription != nil {
		return subscription.err
	}
	return nil
}

// taskPagePayload builds one valid streamed page.
func taskPagePayload(cursor string) string {
	return `{"messages":[{"id":"m1","text":"streamed"}],"queued":[],"nextCursor":"` + cursor + `"}`
}

func TestCreatedTaskClientAdmitsOnlyCreationProvenance(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	if owned, err := fixture.client.Owns(task); err != nil || !owned {
		t.Fatalf("owns(a created task) = %v, %v", owned, err)
	}
	unknown, err := randomUUID()
	if err != nil {
		t.Fatalf("randomUUID: %v", err)
	}
	if owned, err := fixture.client.Owns(unknown); err != nil || owned {
		t.Fatalf("owns(an unknown task) = %v, %v", owned, err)
	}
	if owned, err := fixture.client.Owns("not-a-task"); err != nil || owned {
		t.Fatalf("owns(an unrelated id) = %v, %v", owned, err)
	}
	foreign := fixture.createTaskDirectory()
	if owned, err := fixture.client.Owns(foreign); err != nil || owned {
		t.Fatalf("owns(a task without provenance) = %v, %v", owned, err)
	}
	// A provenance that names no executor is an error, never an admission.
	corrupt := fixture.createTaskDirectory()
	if err := SaveExecutorFile(filepath.Join(fixture.private, "tasks", corrupt), executorOriginName,
		map[string]any{"executorId": "not-an-executor"}); err != nil {
		t.Fatalf("SaveExecutorFile: %v", err)
	}
	if owned, err := fixture.client.Owns(corrupt); err == nil || owned {
		t.Fatalf("owns(a corrupt provenance) = %v, %v", owned, err)
	}
}

func TestCreatedTaskClientRefusesForeignTasksAndAClosedTransport(t *testing.T) {
	fixture := newTaskClientFixture(t)
	foreign := fixture.createTaskDirectory()
	if _, err := fixture.client.Read(context.Background(), foreign, nil, false); err == nil ||
		err.Error() != "Task is outside Filo creation scope" {
		t.Fatalf("read of a foreign task = %v", err)
	}
	if err := fixture.client.Attach(context.Background(), foreign); err == nil ||
		err.Error() != "Task is outside Filo creation scope" {
		t.Fatalf("attach of a foreign task = %v", err)
	}
	if _, err := fixture.client.Send(context.Background(), foreign, "hi", "client-1"); err == nil ||
		err.Error() != "Task is outside Filo creation scope" {
		t.Fatalf("send of a foreign task = %v", err)
	}
	if err := fixture.client.UpdateSettings(context.Background(), foreign, protocol.SessionSettings{}); err == nil ||
		err.Error() != "Task is outside Filo creation scope" {
		t.Fatalf("settings of a foreign task = %v", err)
	}
	if err := fixture.client.Stop(context.Background(), foreign, "turn-1"); err == nil ||
		err.Error() != "Task is outside Filo creation scope" {
		t.Fatalf("stop of a foreign task = %v", err)
	}
	if _, err := fixture.client.Statuses(context.Background(), []string{foreign}); err != nil {
		t.Fatalf("statuses of a foreign task = %v", err)
	}
	if opens, calls := fixture.openList(), fixture.snapshotCalls(); len(opens) != 0 || len(calls) != 0 {
		t.Fatalf("a foreign task reached the wire: %v / %+v", opens, calls)
	}
	task := fixture.createTask()
	fixture.client.Close()
	if owned, err := fixture.client.Owns(task); err == nil || owned ||
		err.Error() != "Filo task client is closed" {
		t.Fatalf("owns after close = %v, %v", owned, err)
	}
	if _, err := fixture.client.Read(context.Background(), task, nil, false); err == nil ||
		err.Error() != "Filo task client is closed" {
		t.Fatalf("read after close = %v", err)
	}
	if err := fixture.client.Attach(context.Background(), task); err == nil ||
		err.Error() != "Filo task client is closed" {
		t.Fatalf("attach after close = %v", err)
	}
	if _, err := fixture.client.Send(context.Background(), task, "hi", "client-1"); err == nil ||
		err.Error() != "Filo task client is closed" {
		t.Fatalf("send after close = %v", err)
	}
	// A closed transport still answers an empty status query in memory.
	if statuses, err := fixture.client.Statuses(context.Background(), nil); err != nil || len(statuses) != 0 {
		t.Fatalf("empty statuses after close = %v, %v", statuses, err)
	}
}

func TestCreatedTaskClientKeepsTheReadWireShape(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	fixture.answer = func(_ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"messages":[],"queued":[],"nextCursor":null}`), nil
	}
	cursor := "next page/+"
	page, err := fixture.client.Read(context.Background(), task, &cursor, true)
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	if page.NextCursor != nil || len(page.Messages) != 0 || len(page.Queued) != 0 {
		t.Fatalf("read page = %+v", page)
	}
	// An empty cursor is an absent cursor, and a reader without one sends only
	// the activity flag: no empty cursor ever reaches the wire.
	empty := ""
	if _, err := fixture.client.Read(context.Background(), task, &empty, false); err != nil {
		t.Fatalf("read page: %v", err)
	}
	want := []string{
		"/v1/sessions/" + task + "?includeActivity=true&cursor=" + url.QueryEscape(cursor),
		"/v1/sessions/" + task + "?includeActivity=false",
	}
	calls := fixture.snapshotCalls()
	if len(calls) != len(want) {
		t.Fatalf("private calls = %+v", calls)
	}
	for index, expected := range want {
		if calls[index].path != expected {
			t.Fatalf("call %d = %q, want %q", index, calls[index].path, expected)
		}
		if calls[index].input != nil {
			t.Fatalf("call %d input = %v", index, calls[index].input)
		}
	}
	if opens := fixture.openList(); len(opens) != 0 {
		t.Fatalf("a plain read opened %v", opens)
	}
}

func TestCreatedTaskClientServesTheSubscribedActivityPage(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	emit := fixture.openScriptedStream()
	if err := fixture.client.Attach(context.Background(), task); err != nil {
		t.Fatalf("attach: %v", err)
	}
	emit("data: " + taskPagePayload("page-2") + "\n\n")
	waitForCondition(t, 5*time.Second, "the first streamed page", func() bool {
		return len(fixture.changeList()) > 0
	})
	page, err := fixture.client.Read(context.Background(), task, nil, true)
	if err != nil {
		t.Fatalf("cached read: %v", err)
	}
	if page.NextCursor == nil || *page.NextCursor != "page-2" || len(page.Messages) != 1 ||
		page.Messages[0].Text != "streamed" {
		t.Fatalf("cached page = %+v", page)
	}
	if calls := fixture.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("a cached page triggered %+v", calls)
	}
	// The cache only serves the first activity page; any other read goes out.
	fixture.answer = func(_ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"messages":[],"queued":[],"nextCursor":null}`), nil
	}
	cursor := "more"
	if _, err := fixture.client.Read(context.Background(), task, &cursor, true); err != nil {
		t.Fatalf("cursor read: %v", err)
	}
	if _, err := fixture.client.Read(context.Background(), task, nil, false); err != nil {
		t.Fatalf("plain read: %v", err)
	}
	if calls := fixture.snapshotCalls(); len(calls) != 2 {
		t.Fatalf("private calls = %+v", calls)
	}
	if changes := fixture.changeList(); len(changes) != 1 || changes[0] != task {
		t.Fatalf("changes = %v", changes)
	}
}

func TestCreatedTaskClientRefusesInvalidPages(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	entries := make([]string, 0, taskClientMessages+1)
	for index := 0; index <= taskClientMessages; index++ {
		entries = append(entries, `{"id":"m","text":"x"}`)
	}
	payloads := []string{
		`[]`,
		`null`,
		`"a page"`,
		`{"queued":[],"nextCursor":null}`,
		`{"messages":[],"nextCursor":null}`,
		`{"messages":null,"queued":[],"nextCursor":null}`,
		`{"messages":[],"queued":{},"nextCursor":null}`,
		`{"messages":[],"queued":[],"nextCursor":42}`,
		`{"messages":[],"queued":[]}`,
		`{"messages":[` + strings.Join(entries, ",") + `],"queued":[],"nextCursor":null}`,
	}
	for _, payload := range payloads {
		raw := json.RawMessage(payload)
		fixture.answer = func(_ string, _ any) (json.RawMessage, error) { return raw, nil }
		if _, err := fixture.client.Read(context.Background(), task, nil, false); err == nil ||
			err.Error() != "Filo returned an invalid task page" {
			t.Fatalf("read of %s = %v", payload, err)
		}
	}
}

func TestCreatedTaskClientBoundsSubscriptionsAndHoldsReferences(t *testing.T) {
	fixture := newTaskClientFixture(t)
	tasks := make([]string, 0, taskClientSubscriptions+1)
	for index := 0; index <= taskClientSubscriptions; index++ {
		tasks = append(tasks, fixture.createTask())
	}
	for index := 0; index < taskClientSubscriptions; index++ {
		fixture.blockStream()
		if err := fixture.client.Attach(context.Background(), tasks[index]); err != nil {
			t.Fatalf("attach %d: %v", index, err)
		}
	}
	// A second attach of one task shares the same stream instead of taking a slot.
	if err := fixture.client.Attach(context.Background(), tasks[0]); err != nil {
		t.Fatalf("shared attach: %v", err)
	}
	fixture.blockStream()
	if err := fixture.client.Attach(context.Background(), tasks[taskClientSubscriptions]); err == nil ||
		err.Error() != "Too many task subscriptions" {
		t.Fatalf("attach beyond the bound = %v", err)
	}
	if count := fixture.subscriptionCount(); count != taskClientSubscriptions {
		t.Fatalf("subscriptions = %d", count)
	}
	fixture.client.Detach(tasks[0])
	if count := fixture.subscriptionCount(); count != taskClientSubscriptions {
		t.Fatalf("subscriptions after a shared detach = %d", count)
	}
	fixture.client.Detach(tasks[0])
	if count := fixture.subscriptionCount(); count != taskClientSubscriptions-1 {
		t.Fatalf("subscriptions after the last detach = %d", count)
	}
	if err := fixture.client.Attach(context.Background(), tasks[taskClientSubscriptions]); err != nil {
		t.Fatalf("attach after a release: %v", err)
	}
	if count := fixture.subscriptionCount(); count != taskClientSubscriptions {
		t.Fatalf("subscriptions after a reattach = %d", count)
	}
	paths := fixture.openList()
	if len(paths) != taskClientSubscriptions+1 {
		t.Fatalf("stream opens = %v", paths)
	}
	for index, path := range paths {
		expected := "/v1/sessions/" + tasks[index] + "/events"
		if path != expected {
			t.Fatalf("stream open %d = %q, want %q", index, path, expected)
		}
	}
}

func TestCreatedTaskClientReportsStreamFailures(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	// A refused stream fails the attach and leaves no subscription behind.
	fixture.scriptStatus(http.StatusServiceUnavailable)
	if err := fixture.client.Attach(context.Background(), task); err == nil ||
		err.Error() != "Filo task stream is unavailable" {
		t.Fatalf("attach to a refused stream = %v", err)
	}
	if count := fixture.subscriptionCount(); count != 0 {
		t.Fatalf("a refused attach left %d subscriptions", count)
	}
	// A stream that ends without an error marker is a failure, and the failure
	// replaces the page it had already delivered.
	fixture.scriptStream("data: " + taskPagePayload("page-2") + "\n\n")
	if err := fixture.client.Attach(context.Background(), task); err != nil {
		t.Fatalf("attach: %v", err)
	}
	waitForCondition(t, 5*time.Second, "the disconnected stream", func() bool {
		return fixture.subscriptionError(task) != nil
	})
	if err := fixture.subscriptionError(task); err.Error() != "Filo task stream disconnected" {
		t.Fatalf("stream failure = %v", err)
	}
	if _, err := fixture.client.Read(context.Background(), task, nil, true); err == nil ||
		err.Error() != "Filo task stream disconnected" {
		t.Fatalf("read after a stream failure = %v", err)
	}
	if len(fixture.changeList()) == 0 {
		t.Fatal("no page change was reported")
	}
	// The error marker is a terminal stream failure of its own.
	marker := fixture.createTask()
	fixture.scriptStream("event: error\n\n")
	if err := fixture.client.Attach(context.Background(), marker); err != nil {
		t.Fatalf("attach: %v", err)
	}
	waitForCondition(t, 5*time.Second, "the error marker", func() bool {
		return fixture.subscriptionError(marker) != nil
	})
	if err := fixture.subscriptionError(marker); err.Error() != "Filo task stream failed" {
		t.Fatalf("error marker = %v", err)
	}
	// A page the guard refuses fails the stream instead of being reported.
	invalid := fixture.createTask()
	fixture.scriptStream("data: []\n\n")
	if err := fixture.client.Attach(context.Background(), invalid); err != nil {
		t.Fatalf("attach: %v", err)
	}
	waitForCondition(t, 5*time.Second, "the refused page", func() bool {
		return fixture.subscriptionError(invalid) != nil
	})
	if err := fixture.subscriptionError(invalid); err.Error() != "Filo returned an invalid task page" {
		t.Fatalf("refused page = %v", err)
	}
}

func TestCreatedTaskClientScopesStatuses(t *testing.T) {
	fixture := newTaskClientFixture(t)
	tasks := []string{fixture.createTask(), fixture.createTask()}
	foreign := fixture.createTaskDirectory()
	tooMany := make([]string, 0, taskClientStatuses+1)
	for index := 0; index <= taskClientStatuses; index++ {
		tooMany = append(tooMany, foreign)
	}
	if _, err := fixture.client.Statuses(context.Background(), tooMany); err == nil ||
		err.Error() != "At most 30 listed sessions" {
		t.Fatalf("statuses beyond the bound = %v", err)
	}
	if statuses, err := fixture.client.Statuses(context.Background(), []string{foreign}); err != nil ||
		len(statuses) != 0 {
		t.Fatalf("statuses of no owned task = %v, %v", statuses, err)
	}
	if calls := fixture.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("private calls = %+v", calls)
	}
	fixture.answer = func(_ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"statuses":[{"id":"` + tasks[0] + `"},{"id":"` + tasks[1] + `"}]}`), nil
	}
	statuses, err := fixture.client.Statuses(context.Background(), []string{tasks[0], foreign, tasks[1]})
	if err != nil || len(statuses) != 2 || statuses[0].ID != tasks[0] || statuses[1].ID != tasks[1] {
		t.Fatalf("statuses = %+v, %v", statuses, err)
	}
	calls := fixture.snapshotCalls()
	if len(calls) != 1 || calls[0].path != "/v1/sessions/status?ids="+tasks[0]+","+tasks[1] {
		t.Fatalf("private calls = %+v", calls)
	}
	// An answer that is short, reordered or not a list is refused whole.
	replies := []string{
		`{"statuses":[{"id":"` + tasks[1] + `"},{"id":"` + tasks[0] + `"}]}`,
		`{"statuses":[{"id":"` + tasks[0] + `"}]}`,
		`{"statuses":"none"}`,
		`{"statuses":[]}`,
	}
	for _, reply := range replies {
		raw := json.RawMessage(reply)
		fixture.answer = func(_ string, _ any) (json.RawMessage, error) { return raw, nil }
		if _, err := fixture.client.Statuses(context.Background(), []string{tasks[0], tasks[1]}); err == nil ||
			err.Error() != "Invalid scoped task statuses" {
			t.Fatalf("statuses of %s = %v", reply, err)
		}
	}
}

func TestCreatedTaskClientRequiresNativeAcknowledgements(t *testing.T) {
	fixture := newTaskClientFixture(t)
	task := fixture.createTask()
	fixture.answer = func(_ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"turnId":"turn-1","clientId":"client-1"}`), nil
	}
	receipt, err := fixture.client.Send(context.Background(), task, "hello", "client-1")
	if err != nil || receipt.TurnID != "turn-1" || receipt.ClientID != "client-1" {
		t.Fatalf("send = %+v, %v", receipt, err)
	}
	calls := fixture.snapshotCalls()
	if calls[0].path != "/v1/sessions/"+task+"/messages" {
		t.Fatalf("send path = %q", calls[0].path)
	}
	payload, ok := calls[0].input.(map[string]any)
	if !ok || payload["text"] != "hello" || payload["clientId"] != "client-1" {
		t.Fatalf("send input = %v", calls[0].input)
	}
	for _, reply := range []string{
		`{"turnId":"turn-2","clientId":"another"}`,
		`{"turnId":"","clientId":"client-1"}`,
		`{"clientId":"client-1"}`,
		`[]`,
	} {
		raw := json.RawMessage(reply)
		fixture.answer = func(_ string, _ any) (json.RawMessage, error) { return raw, nil }
		if _, err := fixture.client.Send(context.Background(), task, "hello", "client-1"); err == nil ||
			err.Error() != "Native send is unconfirmed" {
			t.Fatalf("send of %s = %v", reply, err)
		}
	}
	fixture.answer = func(_ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"updated":true}`), nil
	}
	if err := fixture.client.UpdateSettings(context.Background(), task, protocol.SessionSettings{}); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if calls := fixture.snapshotCalls(); calls[len(calls)-1].path != "/v1/sessions/"+task+"/settings" {
		t.Fatalf("settings path = %q", calls[len(calls)-1].path)
	}
	for _, reply := range []string{`{"updated":false}`, `{}`, `{"updated":"yes"}`, `[]`} {
		raw := json.RawMessage(reply)
		fixture.answer = func(_ string, _ any) (json.RawMessage, error) { return raw, nil }
		if err := fixture.client.UpdateSettings(context.Background(), task,
			protocol.SessionSettings{}); err == nil || err.Error() != "Native settings are unconfirmed" {
			t.Fatalf("update settings of %s = %v", reply, err)
		}
	}
	fixture.answer = func(_ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"stopped":true}`), nil
	}
	if err := fixture.client.Stop(context.Background(), task, "turn-1"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	calls = fixture.snapshotCalls()
	if calls[len(calls)-1].path != "/v1/sessions/"+task+"/stop" {
		t.Fatalf("stop path = %q", calls[len(calls)-1].path)
	}
	for _, reply := range []string{`{"stopped":false}`, `{}`, `{"stopped":"yes"}`} {
		raw := json.RawMessage(reply)
		fixture.answer = func(_ string, _ any) (json.RawMessage, error) { return raw, nil }
		if err := fixture.client.Stop(context.Background(), task, "turn-1"); err == nil ||
			err.Error() != "Native stop is unconfirmed" {
			t.Fatalf("stop of %s = %v", reply, err)
		}
	}
}
