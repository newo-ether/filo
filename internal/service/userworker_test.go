package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

// userWorkerTestTokens is the credential source of one fixture. The gate
// reproduces a lookup that is still loading, so a closure can be ordered against
// it instead of raced with it.
type userWorkerTestTokens struct {
	mu        sync.Mutex
	value     string
	gate      chan struct{}
	entered   chan struct{}
	announced bool
}

func newUserWorkerTestTokens() *userWorkerTestTokens {
	return &userWorkerTestTokens{entered: make(chan struct{})}
}

// Credential resolves one credential, waiting on the gate while a lookup is held.
func (state *userWorkerTestTokens) Credential(ctx context.Context) (string, error) {
	state.mu.Lock()
	entered := state.entered
	if !state.announced {
		state.announced = true
		close(entered)
	}
	gate, value := state.gate, state.value
	state.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		state.mu.Lock()
		value = state.value
		state.mu.Unlock()
	}
	if value == "" {
		return "", errors.New("Missing file")
	}
	return value, nil
}

// set installs the credential the next lookup resolves.
func (state *userWorkerTestTokens) set(value string) {
	state.mu.Lock()
	state.value = value
	state.mu.Unlock()
}

// hold makes the next lookup wait until the returned gate is closed.
func (state *userWorkerTestTokens) hold() chan struct{} {
	gate := make(chan struct{})
	state.mu.Lock()
	state.gate = gate
	state.mu.Unlock()
	return gate
}

// started reports the channel closed by the first lookup, so a test can wait for
// a lookup to be in flight without sleeping.
func (state *userWorkerTestTokens) started() chan struct{} {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.entered
}

// userWorkerTestHistory is the read-only native history behind one fixture. It
// records every argument so a passthrough can be compared with the TS call log.
type userWorkerTestHistory struct {
	mu      sync.Mutex
	calls   [][]any
	failing bool
	catalog protocol.SessionPage
	page    protocol.ConversationPage
}

func (source *userWorkerTestHistory) List(_ context.Context, cursor string) (protocol.SessionPage, error) {
	source.mu.Lock()
	source.calls = append(source.calls, []any{"list", cursor})
	catalog := source.catalog
	source.mu.Unlock()
	return catalog, nil
}

func (source *userWorkerTestHistory) Read(_ context.Context, id, cursor string, activity bool,
	excluded []string) (protocol.ConversationPage, error) {
	source.mu.Lock()
	source.calls = append(source.calls, []any{"read", id, cursor, activity, excluded})
	failing, page := source.failing, source.page
	source.mu.Unlock()
	if failing {
		return protocol.ConversationPage{}, errors.New("Native history unavailable")
	}
	return page, nil
}

func (source *userWorkerTestHistory) recorded() [][]any {
	source.mu.Lock()
	defer source.mu.Unlock()
	return append([][]any(nil), source.calls...)
}

func (source *userWorkerTestHistory) fail() {
	source.mu.Lock()
	source.failing = true
	source.mu.Unlock()
}

// userWorkerUnusedHistory is the history of a fixture whose test must never read
// native storage.
type userWorkerUnusedHistory struct{}

func (userWorkerUnusedHistory) List(context.Context, string) (protocol.SessionPage, error) {
	return protocol.SessionPage{}, errors.New("Unexpected history listing")
}

func (userWorkerUnusedHistory) Read(context.Context, string, string, bool,
	[]string) (protocol.ConversationPage, error) {
	return protocol.ConversationPage{}, errors.New("Unexpected history reading")
}

// userWorkerTestRecord is one requested call of the auxiliary user service.
type userWorkerTestRecord struct {
	path          string
	method        string
	body          string
	authorization string
	createTrace   string
}

// userWorkerTestServer is one loopback user-helper double. The handler receives
// the recorded request and answers it.
type userWorkerTestServer struct {
	server *httptest.Server
	mu     sync.Mutex
	seen   []userWorkerTestRecord
}

func newUserWorkerTestServer(t *testing.T,
	handler func(http.ResponseWriter, *http.Request, userWorkerTestRecord)) *userWorkerTestServer {
	t.Helper()
	server := &userWorkerTestServer{}
	server.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter,
		request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Logf("read request body: %v", err)
		}
		record := userWorkerTestRecord{path: request.URL.Path, method: request.Method,
			body: string(body), authorization: request.Header.Get("Authorization"),
			createTrace: request.Header.Get(createTraceHeader)}
		server.mu.Lock()
		server.seen = append(server.seen, record)
		server.mu.Unlock()
		handler(writer, request, record)
	}))
	t.Cleanup(server.server.Close)
	return server
}

func (server *userWorkerTestServer) url() string { return server.server.URL }

func (server *userWorkerTestServer) calls() []userWorkerTestRecord {
	server.mu.Lock()
	defer server.mu.Unlock()
	return append([]userWorkerTestRecord(nil), server.seen...)
}

func (server *userWorkerTestServer) callCount() int {
	server.mu.Lock()
	defer server.mu.Unlock()
	return len(server.seen)
}

// methods returns the recorded method sequence, which pins that an unknown
// outcome is never retried.
func (server *userWorkerTestServer) methods() []string {
	methods := []string{}
	for _, record := range server.calls() {
		methods = append(methods, record.method)
	}
	return methods
}

// lastAuthorization returns the credential of the newest recorded call.
func (server *userWorkerTestServer) lastAuthorization() string {
	calls := server.calls()
	if len(calls) == 0 {
		return ""
	}
	return calls[len(calls)-1].authorization
}

// writeUserWorkerJSON answers one request. A write to an aborted client is not a
// test failure: a deadline case ends the call while the double is still writing.
func writeUserWorkerJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Errorf("encode response: %v", err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if _, err := writer.Write(encoded); err != nil {
		t.Logf("write response: %v", err)
	}
}

// newUserWorkerFixture admits one worker over a double and closes it with the
// test.
func newUserWorkerFixture(t *testing.T, target string, token UserWorkerToken,
	history UserWorkerHistory, timeouts UserWorkerTimeouts) *UserWorker {
	t.Helper()
	worker, err := NewUserWorker(target, token, history, "", timeouts)
	if err != nil {
		t.Fatalf("NewUserWorker = %v", err)
	}
	t.Cleanup(worker.Close)
	return worker
}

// assertUserWorkerTimeout requires the deadline to have ended the call. The Go
// client reports a request deadline as context.DeadlineExceeded, where the TS
// test matched the "timeout" text.
func assertUserWorkerTimeout(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("call succeeded, want a deadline")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return
	}
	t.Fatalf("error = %v, want a deadline", err)
}

// assertUserWorkerCut requires an unfinished connect to have been cut. The
// connect timer cancels its own context, so the client reports a cancellation
// rather than a deadline.
func assertUserWorkerCut(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("call succeeded, want a cut connect")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return
	}
	t.Fatalf("error = %v, want a cut connect", err)
}

func TestUserWorkerMissingCredentialsFailOnlyThatRequest(t *testing.T) {
	tokens := newUserWorkerTestTokens()
	server := newUserWorkerTestServer(t, func(writer http.ResponseWriter, _ *http.Request,
		_ userWorkerTestRecord) {
		writeUserWorkerJSON(t, writer, http.StatusOK, map[string]any{"models": []any{}})
	})
	worker := newUserWorkerFixture(t, server.url(), tokens, userWorkerUnusedHistory{},
		DefaultUserWorkerTimeouts)

	_, err := worker.Models(context.Background())
	assertRpcRefusal(t, err, "Filo user helper is not ready. Repair its installation or sign in "+
		"to the selected Windows account.", -32000)
	if server.callCount() != 0 {
		t.Fatalf("calls = %d, want 0", server.callCount())
	}
	tokens.set(strings.Repeat("a", 64))
	models, err := worker.Models(context.Background())
	if err != nil {
		t.Fatalf("models = %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("models = %+v, want none", models)
	}
	if server.callCount() != 1 {
		t.Fatalf("calls = %d, want 1", server.callCount())
	}
	if authorization := server.lastAuthorization(); authorization != "Bearer "+strings.Repeat("a", 64) {
		t.Fatalf("authorization = %q", authorization)
	}
}

func TestUserWorkerCloseDuringCredentialLoadingPreventsALateOperation(t *testing.T) {
	tokens := newUserWorkerTestTokens()
	gate := tokens.hold()
	server := newUserWorkerTestServer(t, func(writer http.ResponseWriter, _ *http.Request,
		_ userWorkerTestRecord) {
		writeUserWorkerJSON(t, writer, http.StatusOK, map[string]any{})
	})
	worker := newUserWorkerFixture(t, server.url(), tokens, userWorkerUnusedHistory{},
		DefaultUserWorkerTimeouts)

	pending := make(chan error, 1)
	go func() {
		_, _, err := worker.Create(context.Background(), "hello", "client", protocol.SessionSettings{})
		pending <- err
	}()
	select {
	case <-tokens.started():
	case <-time.After(10 * time.Second):
		t.Fatal("the credential lookup never started")
	}
	worker.Close()
	tokens.set(strings.Repeat("a", 64))
	close(gate)
	err := <-pending
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("create = %v, want a closure", err)
	}
	if server.callCount() != 0 {
		t.Fatalf("calls = %d, want 0", server.callCount())
	}
}

func TestUserWorkerRejectsExternalEndpointsAndNeverRetries(t *testing.T) {
	token := strings.Repeat("a", 64)
	for _, target := range []string{
		"http://100.100.10.2:7435",
		"http://localhost:7435",
		"https://127.0.0.1:7435",
		"http://127.0.0.1:7435/prefix",
		"http://user:pass@127.0.0.1:7435",
		"http://[::1]:7435",
	} {
		worker, err := NewUserWorker(target, StaticUserWorkerToken(token), userWorkerUnusedHistory{},
			"", DefaultUserWorkerTimeouts)
		if err == nil {
			worker.Close()
			t.Fatalf("NewUserWorker(%q) was admitted", target)
		}
		if !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("NewUserWorker(%q) = %v", target, err)
		}
	}
	if _, err := NewUserWorker("http://127.0.0.1:7435", StaticUserWorkerToken("short"),
		userWorkerUnusedHistory{}, "", DefaultUserWorkerTimeouts); err == nil ||
		!strings.Contains(err.Error(), "loopback") {
		t.Fatalf("a short credential was admitted: %v", err)
	}

	server := newUserWorkerTestServer(t, func(writer http.ResponseWriter, _ *http.Request,
		_ userWorkerTestRecord) {
		writeUserWorkerJSON(t, writer, http.StatusConflict,
			map[string]any{"error": "already has an active writer"})
	})
	worker := newUserWorkerFixture(t, server.url(), StaticUserWorkerToken(token),
		userWorkerUnusedHistory{}, DefaultUserWorkerTimeouts)
	if worker.NewTasks() != nil {
		t.Fatal("a worker without a task directory owns a task transport")
	}
	_, err := worker.Archive(context.Background(), "thread")
	assertRpcRefusal(t, err, "already has an active writer", -32600)
	if server.callCount() != 1 {
		t.Fatalf("calls = %d, want 1", server.callCount())
	}
	if authorization := server.lastAuthorization(); authorization != "Bearer "+token {
		t.Fatalf("authorization = %q", authorization)
	}

	taskWorker, err := NewUserWorker(server.url(), StaticUserWorkerToken(token),
		userWorkerUnusedHistory{}, t.TempDir(), DefaultUserWorkerTimeouts)
	if err != nil {
		t.Fatalf("NewUserWorker with a task directory = %v", err)
	}
	defer taskWorker.Close()
	if taskWorker.NewTasks() == nil {
		t.Fatal("a worker with a task directory owns no task transport")
	}
}

func TestUserWorkerHistoryKeepsNativePagesAndNeverContactsTheAuxiliary(t *testing.T) {
	history := &userWorkerTestHistory{
		catalog: protocol.SessionPage{Sessions: []protocol.Session{},
			NextCursor: pointerTo("catalog-page")},
		page: protocol.ConversationPage{
			Messages: []protocol.Message{{
				MessageIdentity: protocol.MessageIdentity{ID: "m0", TurnID: "turn",
					ClientID: pointerTo("client"), Role: "assistant", Timestamp: protocol.Number(1)},
				Text: protocol.Text("Native"),
			}},
			Queued:     []protocol.QueuedInput{},
			NextCursor: pointerTo("earlier"),
		},
	}
	server := newUserWorkerTestServer(t, func(writer http.ResponseWriter, _ *http.Request,
		_ userWorkerTestRecord) {
		writeUserWorkerJSON(t, writer, http.StatusOK, map[string]any{})
	})
	worker := newUserWorkerFixture(t, server.url(), StaticUserWorkerToken(strings.Repeat("a", 64)),
		history, DefaultUserWorkerTimeouts)

	catalog, err := worker.List(context.Background(), "catalog")
	if err != nil {
		t.Fatalf("list = %v", err)
	}
	if catalog.NextCursor == nil || *catalog.NextCursor != "catalog-page" {
		t.Fatalf("catalog = %+v", catalog)
	}
	page, err := worker.Read(context.Background(), "thread", "before", true, []string{"live"})
	if err != nil {
		t.Fatalf("read = %v", err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != "m0" {
		t.Fatalf("page = %+v", page)
	}
	want := [][]any{{"list", "catalog"}, {"read", "thread", "before", true, []string{"live"}}}
	if calls := history.recorded(); !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}

	// A worker cursor is decoded before the native read, so the pager never lets
	// one namespace collide with the desktop layer of the same task.
	cursor := encodePageCursor("filo.page.worker.", pageCursorPayload{ID: "thread",
		Native: pointerTo("native"), Through: pointerTo("m0")})
	paged, err := worker.Read(context.Background(), "thread", cursor, false, nil)
	if err != nil {
		t.Fatalf("paged read = %v", err)
	}
	if len(paged.Messages) != 1 || paged.Messages[0].ID != "m0" {
		t.Fatalf("paged = %+v", paged)
	}
	calls := history.recorded()
	if native := calls[len(calls)-1][2]; native != "native" {
		t.Fatalf("native cursor = %v, want the decoded worker cursor", native)
	}

	history.fail()
	_, err = worker.Read(context.Background(), "thread", "", false, nil)
	if err == nil || err.Error() != "Native history unavailable" {
		t.Fatalf("read = %v, want the native failure", err)
	}
	if server.callCount() != 0 {
		t.Fatalf("auxiliary requests = %d, want 0", server.callCount())
	}
}

func TestUserWorkerPeripheralRequestsPreserveAuthenticatedReceipts(t *testing.T) {
	const createClientID = "0b5e3c2a-7f6d-4c8e-9a2b-1d4e5f6a7b8c"
	session := protocol.Session{ID: taskSessionsTaskID, Title: protocol.Text("Native title"),
		Cwd: "/fixture", UpdatedAt: 1}
	acknowledge := true
	server := newUserWorkerTestServer(t, func(writer http.ResponseWriter, request *http.Request,
		_ userWorkerTestRecord) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/archive"):
			writeUserWorkerJSON(t, writer, http.StatusOK,
				map[string]any{"archived": acknowledge, "cwd": session.Cwd})
		case request.URL.Path == "/v1/models":
			writeUserWorkerJSON(t, writer, http.StatusOK, map[string]any{"models": []any{}})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/sessions":
			writeUserWorkerJSON(t, writer, http.StatusCreated, map[string]any{"id": session.ID,
				"title": string(session.Title), "cwd": session.Cwd, "updatedAt": session.UpdatedAt,
				"turnId": taskSessionsTurnID, "clientId": createClientID})
		default:
			writeUserWorkerJSON(t, writer, http.StatusOK, session)
		}
	})
	token := strings.Repeat("a", 64)
	worker := newUserWorkerFixture(t, server.url(), StaticUserWorkerToken(token),
		userWorkerUnusedHistory{}, DefaultUserWorkerTimeouts)

	created, receipt, err := worker.Create(context.Background(), "hello", createClientID, protocol.SessionSettings{})
	if err != nil {
		t.Fatalf("create = %v", err)
	}
	if created.ID != session.ID || string(created.Title) != "Native title" || created.Cwd != "/fixture" ||
		created.UpdatedAt != 1 {
		t.Fatalf("created = %+v", created)
	}
	if receipt.TurnID != taskSessionsTurnID || receipt.ClientID != createClientID {
		t.Fatalf("create receipt = %+v", receipt)
	}
	renamed, err := worker.Rename(context.Background(), "thread", "Requested title")
	if err != nil {
		t.Fatalf("rename = %v", err)
	}
	if renamed.ID != session.ID {
		t.Fatalf("renamed = %+v", renamed)
	}
	cwd, err := worker.Archive(context.Background(), "thread")
	if err != nil || cwd != "/fixture" {
		t.Fatalf("archive = %q/%v", cwd, err)
	}
	models, err := worker.Models(context.Background())
	if err != nil || len(models) != 0 {
		t.Fatalf("models = %+v/%v", models, err)
	}
	want := []userWorkerTestRecord{
		{path: "/v1/sessions", method: "POST",
			body:          `{"clientId":"0b5e3c2a-7f6d-4c8e-9a2b-1d4e5f6a7b8c","text":"hello"}`,
			authorization: "Bearer " + token},
		{path: "/v1/sessions/thread/rename", method: "POST", body: `{"name":"Requested title"}`,
			authorization: "Bearer " + token},
		{path: "/v1/sessions/thread/archive", method: "POST", body: "{}",
			authorization: "Bearer " + token},
		{path: "/v1/models", method: "GET", body: "", authorization: "Bearer " + token},
	}
	if calls := server.calls(); !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}

	acknowledge = false
	_, err = worker.Archive(context.Background(), "thread")
	if err == nil || !strings.Contains(err.Error(), "unconfirmed") {
		t.Fatalf("archive = %v, want an unconfirmed receipt", err)
	}
	if server.callCount() != 5 {
		t.Fatalf("calls = %d, want 5", server.callCount())
	}
}

func TestUserWorkerColdMutationsOutliveTheReadDeadlineWithoutRetrying(t *testing.T) {
	const createClientID = "0b5e3c2a-7f6d-4c8e-9a2b-1d4e5f6a7b8c"
	var mu sync.Mutex
	slow := false
	server := newUserWorkerTestServer(t, func(writer http.ResponseWriter, request *http.Request,
		_ userWorkerTestRecord) {
		mu.Lock()
		delay := 150 * time.Millisecond
		if slow {
			delay = 800 * time.Millisecond
		}
		mu.Unlock()
		time.Sleep(delay)
		if request.Method == http.MethodPost {
			writeUserWorkerJSON(t, writer, http.StatusOK, map[string]any{"id": taskSessionsTaskID,
				"title": "Created", "cwd": "/fixture", "updatedAt": 1,
				"turnId": taskSessionsTurnID, "clientId": createClientID})
			return
		}
		writeUserWorkerJSON(t, writer, http.StatusOK, map[string]any{"models": []any{}})
	})
	worker := newUserWorkerFixture(t, server.url(), StaticUserWorkerToken(strings.Repeat("a", 64)),
		userWorkerUnusedHistory{},
		UserWorkerTimeouts{Read: 50 * time.Millisecond, Mutation: 500 * time.Millisecond})

	created, _, err := worker.Create(context.Background(), "hello", createClientID, protocol.SessionSettings{})
	if err != nil {
		t.Fatalf("create = %v", err)
	}
	if created.ID != taskSessionsTaskID {
		t.Fatalf("created = %+v", created)
	}
	if _, err := worker.Models(context.Background()); err == nil {
		t.Fatal("models outlived the read deadline")
	} else {
		assertUserWorkerTimeout(t, err)
	}
	mu.Lock()
	slow = true
	mu.Unlock()
	if _, _, err := worker.Create(context.Background(), "hello", createClientID, protocol.SessionSettings{}); err == nil {
		t.Fatal("create outlived the mutation deadline")
	} else {
		assertUserWorkerTimeout(t, err)
	}
	if methods := server.methods(); !reflect.DeepEqual(methods, []string{"POST", "GET", "POST"}) {
		t.Fatalf("methods = %v, want no retry", methods)
	}
}

func TestUserWorkerStreamReadDeadlineCoversTheConnectOnly(t *testing.T) {
	server := newUserWorkerTestServer(t, func(writer http.ResponseWriter, _ *http.Request,
		_ userWorkerTestRecord) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		if _, err := writer.Write([]byte("data: ok\n\n")); err != nil {
			t.Logf("write stream: %v", err)
		}
	})
	worker := newUserWorkerFixture(t, server.url(), StaticUserWorkerToken(strings.Repeat("a", 64)),
		userWorkerUnusedHistory{},
		UserWorkerTimeouts{Read: 50 * time.Millisecond, Mutation: time.Second})

	response, err := worker.openStream(context.Background(), "/v1/tasks/thread/events")
	if err != nil {
		t.Fatalf("openStream = %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read stream = %v", err)
	}
	if string(body) != "data: ok\n\n" {
		t.Fatalf("stream = %q", string(body))
	}
	if authorization := server.lastAuthorization(); authorization != "Bearer "+strings.Repeat("a", 64) {
		t.Fatalf("authorization = %q", authorization)
	}

	// A connect that never answers is still cut, so a dead helper cannot park a
	// subscription forever.
	unanswered := newUserWorkerTestServer(t, func(writer http.ResponseWriter, _ *http.Request,
		_ userWorkerTestRecord) {
		time.Sleep(200 * time.Millisecond)
		writer.WriteHeader(http.StatusOK)
	})
	waitingWorker := newUserWorkerFixture(t, unanswered.url(),
		StaticUserWorkerToken(strings.Repeat("a", 64)), userWorkerUnusedHistory{},
		UserWorkerTimeouts{Read: 50 * time.Millisecond, Mutation: time.Second})
	if _, err := waitingWorker.openStream(context.Background(), "/v1/tasks/thread/events"); err == nil {
		t.Fatal("an unanswered connect was not cut")
	} else {
		assertUserWorkerCut(t, err)
	}
}
