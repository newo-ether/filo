package service

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

const testToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// fakeSessions is the bounded session surface the HTTP layer drives in tests.
// Every native touch is counted, so a read-only request or a refused mutation
// can prove it never reached the native layer.
type fakeSessions struct {
	mu        sync.Mutex
	page      protocol.ConversationPage
	sessions  []protocol.Session
	models    []protocol.Model
	createErr error
	traces    []string
	reads     int
	attaches  int
	releases  int
	mutations int
	cwd       string
	sends     []string
	settings  []protocol.SessionSettings
}

func newFakeSessions(count int) *fakeSessions {
	reader, _ := payloadFixture(count)
	return &fakeSessions{page: reader.page}
}

func (f *fakeSessions) List(context.Context, string) (protocol.SessionPage, error) {
	return protocol.SessionPage{Sessions: f.sessions}, nil
}

func (f *fakeSessions) Create(ctx context.Context, text, clientID string, settings protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	f.mu.Lock()
	f.mutations++
	f.traces = append(f.traces, createTraceID(ctx))
	err := f.createErr
	f.mu.Unlock()
	if err != nil {
		return protocol.Session{}, protocol.SendReceipt{}, err
	}
	return protocol.Session{ID: payloadSession, UpdatedAt: 1},
		protocol.SendReceipt{TurnID: "turn-1", ClientID: clientID}, nil
}

func (f *fakeSessions) Models(context.Context) ([]protocol.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.models, nil
}

func (f *fakeSessions) Read(_ context.Context, _ string, _ string, _ bool,
	_ []string) (protocol.ConversationPage, error) {
	f.mu.Lock()
	page := f.page
	page.Messages = append([]protocol.Message(nil), f.page.Messages...)
	f.mu.Unlock()
	f.bump(&f.reads)
	return page, nil
}

// appendMessage grows the shared page so a follower test can report a real
// change through the hub.
func (f *fakeSessions) appendMessage(message protocol.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.page.Messages = append(f.page.Messages, message)
}

func (f *fakeSessions) Attach(context.Context, string) (func(), error) {
	f.bump(&f.attaches)
	return func() { f.bump(&f.releases) }, nil
}

func (f *fakeSessions) Send(_ context.Context, id, text, clientID string) (protocol.SendReceipt, error) {
	f.bump(&f.mutations)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, text)
	return protocol.SendReceipt{TurnID: id, ClientID: clientID}, nil
}

func (f *fakeSessions) SetModel(context.Context, string, string) error {
	f.bump(&f.mutations)
	return nil
}

func (f *fakeSessions) Stop(context.Context, string, string) error {
	f.bump(&f.mutations)
	return nil
}

func (f *fakeSessions) UpdateSettings(_ context.Context, _ string, settings protocol.SessionSettings) error {
	f.bump(&f.mutations)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings = append(f.settings, settings)
	return nil
}

func (f *fakeSessions) Rename(_ context.Context, id, name string) (protocol.Session, error) {
	f.bump(&f.mutations)
	return protocol.Session{ID: id, Title: protocol.Text(name), UpdatedAt: 1}, nil
}

func (f *fakeSessions) Archive(context.Context, string) (string, error) {
	f.bump(&f.mutations)
	return f.cwd, nil
}

func (f *fakeSessions) bump(counter *int) {
	f.mu.Lock()
	*counter++
	f.mu.Unlock()
}

func (f *fakeSessions) counts() (reads, attaches, mutations int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads, f.attaches, f.mutations
}
func (f *fakeSessions) createTraceValues() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.traces...)
}

// statusSessions adds the optional live status surface.
type statusSessions struct {
	*fakeSessions
	ids []string
}

func (s *statusSessions) Statuses(_ context.Context, ids []string) ([]protocol.SessionStatus, error) {
	s.mu.Lock()
	s.ids = append([]string(nil), ids...)
	s.mu.Unlock()
	statuses := make([]protocol.SessionStatus, 0, len(ids))
	for _, id := range ids {
		statuses = append(statuses, protocol.SessionStatus{ID: id})
	}
	return statuses, nil
}

// shutdownSessions adds the optional safe shutdown capability.
type shutdownSessions struct {
	*fakeSessions
	prepared int
}

func (s *shutdownSessions) PrepareShutdown(context.Context) error {
	s.prepared++
	return nil
}

func newTestServer(t *testing.T, options ServerOptions) *httptest.Server {
	t.Helper()
	if options.Token == "" {
		options.Token = testToken
	}
	server, err := NewServer(options)
	if err != nil {
		t.Fatalf("server = %v", err)
	}
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, url string, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request = %v", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do = %v", err)
	}
	return response
}

func post(t *testing.T, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request = %v", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do = %v", err)
	}
	return response
}

func authHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + testToken}
}

func decode(t *testing.T, response *http.Response, value any) {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read = %v", err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatalf("decode %s = %v", data, err)
	}
}

func TestTopologyAndPayloadsAuthenticateBeforeNativeAccess(t *testing.T) {
	fake := newFakeSessions(2)
	ts := newTestServer(t, ServerOptions{Sessions: fake})
	base := ts.URL + "/v1/sessions/" + payloadSession

	if response := get(t, base+"/topology", nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}
	if reads, attaches, mutations := fake.counts(); reads != 0 || attaches != 0 || mutations != 0 {
		t.Fatalf("anonymous request touched native state: %d/%d/%d", reads, attaches, mutations)
	}
	if response := get(t, base+"/topology", map[string]string{"Origin": "http://evil"}); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-origin status = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}

	var topology protocol.TopologyPage
	decode(t, get(t, base+"/topology", authHeaders()), &topology)
	if len(topology.Nodes) != 2 {
		t.Fatalf("nodes = %d", len(topology.Nodes))
	}
	identities := make([]map[string]string, 0, len(topology.Nodes))
	for _, node := range topology.Nodes {
		identities = append(identities, map[string]string{"id": node.ID, "revision": node.Revision})
	}
	query := url.QueryEscape(string(Marshal(identities)))
	var payload PayloadResponse
	decode(t, get(t, base+"/payloads?messages="+query, authHeaders()), &payload)
	if len(payload.Messages) != 2 || string(payload.Messages[0].Text) != "private body 0" {
		t.Fatalf("payloads = %s", Marshal(payload))
	}
	if reads, attaches, mutations := fake.counts(); mutations != 0 || attaches != 0 || reads == 0 {
		t.Fatalf("read path used %d reads, %d attaches, %d mutations", reads, attaches, mutations)
	}
}

func TestInfoAdvertisesIdentityAndCapabilities(t *testing.T) {
	fake := &shutdownSessions{fakeSessions: newFakeSessions(1)}
	desktop := newTestServer(t, ServerOptions{Sessions: fake, Hostname: "host"})
	var plain map[string]any
	decode(t, get(t, desktop.URL+"/v1/info", authHeaders()), &plain)
	if plain["sessionMode"] != "existing" || plain["device"] != "host" ||
		plain["supportsSafeShutdown"] != true || plain["protocolVersion"] != float64(2) {
		t.Fatalf("info = %v", plain)
	}
	if _, present := plain["desktop"]; present {
		t.Fatalf("desktop status leaked: %v", plain)
	}
	worker := newTestServer(t, ServerOptions{Sessions: fake, Hostname: "host",
		Identity: &ServiceIdentity{SessionMode: "standalone", Device: "tinybox"},
		DesktopStatus: func() DesktopStatus {
			message := "offline"
			return DesktopStatus{State: "unavailable", Error: &message}
		}})
	var identity map[string]any
	decode(t, get(t, worker.URL+"/v1/info", authHeaders()), &identity)
	if identity["sessionMode"] != "standalone" || identity["device"] != "tinybox" {
		t.Fatalf("identity = %v", identity)
	}
	desktopState, ok := identity["desktop"].(map[string]any)
	if !ok || desktopState["state"] != "unavailable" || desktopState["error"] != "offline" {
		t.Fatalf("desktop = %v", identity["desktop"])
	}
}

func TestWorkerLayerExcludesTurnsAndBoundsStatusLists(t *testing.T) {
	fake := &statusSessions{fakeSessions: newFakeSessions(1)}
	desktop := newTestServer(t, ServerOptions{Sessions: fake})
	worker := newTestServer(t, ServerOptions{Sessions: fake,
		Identity: &ServiceIdentity{SessionMode: "standalone", Device: "tinybox"}})
	session := "/v1/sessions/" + payloadSession

	// The desktop layer ignores the worker-only parameter entirely.
	if response := get(t, desktop.URL+session+"?excludeTurns=not-a-turn", authHeaders()); response.StatusCode != http.StatusOK {
		t.Fatalf("desktop status = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}
	if response := get(t, worker.URL+session+"?excludeTurns=not-a-turn", authHeaders()); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("worker status = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}

	ids := make([]string, 0, 13)
	for index := 0; index < 13; index++ {
		ids = append(ids, "11111111-1111-4111-8111-"+strconv.Itoa(100000000000+index))
	}
	joined := strings.Join(ids, ",")
	if response := get(t, desktop.URL+"/v1/sessions/status?ids="+joined, authHeaders()); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("desktop status list = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}
	var statuses statusesResponse
	decode(t, get(t, worker.URL+"/v1/sessions/status?ids="+joined, authHeaders()), &statuses)
	if len(statuses.Statuses) != 13 {
		t.Fatalf("statuses = %d", len(statuses.Statuses))
	}
	if response := get(t, desktop.URL+"/v1/sessions/status?ids="+ids[0], authHeaders()); response.StatusCode != http.StatusOK {
		t.Fatalf("single status = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}
}

func TestStatusRouteIsUnavailableWithoutTheCapability(t *testing.T) {
	ts := newTestServer(t, ServerOptions{Sessions: newFakeSessions(1)})
	id := "11111111-1111-4111-8111-111111111111"
	response := get(t, ts.URL+"/v1/sessions/status?ids="+id, authHeaders())
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestSessionCollectionRoutesListCreateAndModels(t *testing.T) {
	fake := newFakeSessions(1)
	fake.models = []protocol.Model{{ID: "gpt-5", Name: protocol.Text("GPT-5")}}
	ts := newTestServer(t, ServerOptions{Sessions: fake})

	var page protocol.SessionPage
	decode(t, get(t, ts.URL+"/v1/sessions", authHeaders()), &page)
	if len(page.Sessions) != 0 || page.NextCursor != nil {
		t.Fatalf("list = %s", Marshal(page))
	}
	created := post(t, ts.URL+"/v1/sessions", `{"text":"hello","clientId":"`+payloadSession+`"}`, authHeaders())
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", created.StatusCode)
	}
	var session protocol.Session
	decode(t, created, &session)
	if session.ID != payloadSession {
		t.Fatalf("created = %s", Marshal(session))
	}
	var models modelsResponse
	decode(t, get(t, ts.URL+"/v1/models", authHeaders()), &models)
	if len(models.Models) != 1 || models.Models[0].ID != "gpt-5" {
		t.Fatalf("models = %s", Marshal(models))
	}
	if _, _, mutations := fake.counts(); mutations != 1 {
		t.Fatalf("mutations = %d", mutations)
	}
}

func TestSessionMutationsValidateBeforeReachingNativeWrites(t *testing.T) {
	fake := newFakeSessions(1)
	ts := newTestServer(t, ServerOptions{Sessions: fake})
	base := ts.URL + "/v1/sessions/" + payloadSession
	client := "11111111-1111-4111-8111-111111111111"

	cases := []struct {
		path string
		body string
	}{
		{"/messages", `{"text":"hello","clientId":"not-a-uuid"}`},
		{"/messages", `{"text":"   ","clientId":"` + client + `"}`},
		{"/rename", `{"name":"  "}`},
		{"/rename", `{"name":42}`},
		{"/model", `{"model":"  "}`},
		{"/stop", `{"turnId":""}`},
	}
	for _, test := range cases {
		response := post(t, base+test.path, test.body, authHeaders())
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s %s status = %d", test.path, test.body, response.StatusCode)
		}
		response.Body.Close()
	}
	// A settings body is validated by the codex layer, so its refusal is the
	// bounded RPC conflict the TS server reports through its outer handler.
	settingsRefusal := post(t, base+"/settings", `{"effort":"high","unknown":1}`, authHeaders())
	if settingsRefusal.StatusCode != http.StatusConflict {
		t.Fatalf("settings refusal status = %d", settingsRefusal.StatusCode)
	}
	settingsRefusal.Body.Close()
	// Every POST action parses a JSON object body before it reaches the session
	// surface, so an empty or non-object body is refused the same way.
	for _, body := range []string{"", "not json", "[]"} {
		response := post(t, base+"/archive", body, authHeaders())
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("archive %q status = %d", body, response.StatusCode)
		}
		response.Body.Close()
	}
	if _, _, mutations := fake.counts(); mutations != 0 {
		t.Fatalf("refused requests reached %d native writes", mutations)
	}

	var renamed protocol.Session
	decode(t, post(t, base+"/rename", `{"name":" kept as sent "}`, authHeaders()), &renamed)
	if renamed.ID != payloadSession || renamed.Title != " kept as sent " {
		t.Fatalf("rename = %s", Marshal(renamed))
	}
	if response := post(t, base+"/model", `{"model":"gpt-5"}`, authHeaders()); response.StatusCode != http.StatusOK {
		t.Fatalf("model status = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}
	if response := post(t, base+"/settings", `{"model":"gpt-5","serviceTier":null}`, authHeaders()); response.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}
	if response := post(t, base+"/stop", `{"turnId":"turn-1"}`, authHeaders()); response.StatusCode != http.StatusOK {
		t.Fatalf("stop status = %d", response.StatusCode)
	} else {
		response.Body.Close()
	}

	var receipt protocol.SendReceipt
	sent := post(t, base+"/messages", `{"text":"hello","clientId":"`+client+`"}`, authHeaders())
	if sent.StatusCode != http.StatusAccepted {
		t.Fatalf("send status = %d", sent.StatusCode)
	}
	decode(t, sent, &receipt)
	if receipt.ClientID != client {
		t.Fatalf("receipt = %s", Marshal(receipt))
	}
	var archived archiveResponse
	decode(t, post(t, base+"/archive", "{}", authHeaders()), &archived)
	if !archived.Archived {
		t.Fatalf("archive = %s", Marshal(archived))
	}
	if _, _, mutations := fake.counts(); mutations != 6 {
		t.Fatalf("mutations = %d", mutations)
	}
}

// shutdownHookCount counts the shutdown hooks one test observes. The route
// answers, flushes and only then runs its hook, so the count is awaited instead
// of read the moment the response returns.
type shutdownHookCount struct {
	mu    sync.Mutex
	count int
}

func (hooks *shutdownHookCount) add() {
	hooks.mu.Lock()
	hooks.count++
	hooks.mu.Unlock()
}

func (hooks *shutdownHookCount) read() int {
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	return hooks.count
}

// The TS route keys off the shutdown hook, not off the prepareShutdown
// capability: a service with a hook but without the capability still answers
// and simply skips the native preparation step.
func TestShutdownRequiresTheHookAndALocalCaller(t *testing.T) {
	fake := &shutdownSessions{fakeSessions: newFakeSessions(1)}
	stopped := &shutdownHookCount{}
	ts := newTestServer(t, ServerOptions{Sessions: fake, OnShutdown: stopped.add})
	response := post(t, ts.URL+"/v1/shutdown", "", authHeaders())
	if response.StatusCode != http.StatusOK {
		t.Fatalf("shutdown status = %d", response.StatusCode)
	}
	var value map[string]any
	decode(t, response, &value)
	waitForCondition(t, 10*time.Second, "the shutdown hook", func() bool { return stopped.read() == 1 })
	if value["stopped"] != true || fake.prepared != 1 {
		t.Fatalf("shutdown = %v, prepared = %d", value, fake.prepared)
	}

	plain := newTestServer(t, ServerOptions{Sessions: newFakeSessions(1), OnShutdown: stopped.add})
	hook := post(t, plain.URL+"/v1/shutdown", "", authHeaders())
	if hook.StatusCode != http.StatusOK {
		t.Fatalf("hooked status = %d", hook.StatusCode)
	}
	hook.Body.Close()
	waitForCondition(t, 10*time.Second, "the second shutdown hook",
		func() bool { return stopped.read() == 2 })

	unhooked := newTestServer(t, ServerOptions{Sessions: newFakeSessions(1)})
	refused := post(t, unhooked.URL+"/v1/shutdown", "", authHeaders())
	if refused.StatusCode != http.StatusNotFound {
		t.Fatalf("unhooked status = %d", refused.StatusCode)
	}
	refused.Body.Close()
}

func TestUnknownRoutesAndIdentitiesAreRejected(t *testing.T) {
	ts := newTestServer(t, ServerOptions{Sessions: newFakeSessions(1)})
	for _, path := range []string{
		"/v1/sessions/not-a-uuid",
		"/v1/sessions/" + payloadSession + "/nonsense",
		"/v1/sessions/" + payloadSession + "/topology/extra",
		"/v1/nothing",
	} {
		response := get(t, ts.URL+path, authHeaders())
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s status = %d", path, response.StatusCode)
		}
		response.Body.Close()
	}
}

func TestEventStreamSendsOneSnapshotPerChange(t *testing.T) {
	fake := newFakeSessions(2)
	hub := NewHub()
	ts := newTestServer(t, ServerOptions{Sessions: fake, Events: hub})
	request, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/sessions/"+payloadSession+"/events?view=topology", nil)
	if err != nil {
		t.Fatalf("request = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("do = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		!strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream = %d %s", response.StatusCode, response.Header.Get("Content-Type"))
	}
	frames := streamFrames(bufio.NewReader(response.Body))

	first := expectFrame(t, frames, "the initial snapshot")
	if !strings.Contains(first, `"nodes"`) || strings.Contains(first, "private body") {
		t.Fatalf("topology frame = %s", first)
	}
	// A change reported for another session never reaches this stream, and a
	// repeated change for the same session coalesces into no second frame.
	hub.Changed("11111111-1111-4111-8111-999999999999")
	expectNoFrame(t, frames, "an unrelated session")
	hub.Changed(payloadSession)
	expectNoFrame(t, frames, "an unchanged snapshot")

	extra := protocol.Message{
		MessageIdentity: protocol.MessageIdentity{
			ID:        "extra",
			TurnID:    "turn",
			Role:      "assistant",
			Timestamp: protocol.Number(99),
		},
		Text: protocol.Text("private body extra"),
	}
	fake.appendMessage(extra)
	hub.Changed(payloadSession)
	second := expectFrame(t, frames, "a reported change")
	if !strings.Contains(second, `"extra"`) || strings.Contains(second, "private body") {
		t.Fatalf("changed frame = %s", second)
	}

	hub.Reset()
	expectClosed(t, frames, "the reset source")

	request, err = http.NewRequest(http.MethodGet, ts.URL+"/v1/sessions/"+payloadSession+"/events?view=topology", nil)
	if err != nil {
		t.Fatalf("replacement request = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	replacement, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("replacement do = %v", err)
	}
	defer replacement.Body.Close()
	replacementFrames := streamFrames(bufio.NewReader(replacement.Body))
	expectFrame(t, replacementFrames, "the replacement snapshot")
	fake.appendMessage(protocol.Message{
		MessageIdentity: protocol.MessageIdentity{
			ID:        "after-reset",
			TurnID:    "turn",
			Role:      "assistant",
			Timestamp: protocol.Number(100),
		},
		Text: protocol.Text("after reset"),
	})
	hub.Changed(payloadSession)
	changed := expectFrame(t, replacementFrames, "a change after reset")
	if !strings.Contains(changed, `"after-reset"`) {
		t.Fatalf("replacement frame = %s", changed)
	}
	hub.Close()
	expectClosed(t, replacementFrames, "the terminally closed source")
	closed := hub.Subscribe(payloadSession)
	expectSubscriptionClosed(t, closed, "a subscription after terminal close")
	if _, attaches, _ := fake.counts(); attaches != 2 {
		t.Fatalf("attaches = %d", attaches)
	}
}

func expectSubscriptionClosed(t *testing.T, subscription *Subscription, what string) {
	t.Helper()
	select {
	case <-subscription.Closed():
	case <-time.After(3 * time.Second):
		t.Fatalf("%s stayed open", what)
	}
}

// readEvent returns the next data frame, skipping comments and blank lines.
func readEvent(reader *bufio.Reader) (string, error) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		if text, ok := strings.CutPrefix(line, "data: "); ok {
			return strings.TrimSpace(text), nil
		}
	}
}

func streamFrames(reader *bufio.Reader) <-chan string {
	frames := make(chan string, 16)
	go func() {
		defer close(frames)
		for {
			text, err := readEvent(reader)
			if err != nil {
				return
			}
			frames <- text
		}
	}()
	return frames
}

func expectFrame(t *testing.T, frames <-chan string, what string) string {
	t.Helper()
	select {
	case text, open := <-frames:
		if !open {
			t.Fatalf("%s: the stream ended instead of sending a frame", what)
		}
		return text
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: no frame arrived", what)
	}
	return ""
}

func expectNoFrame(t *testing.T, frames <-chan string, what string) {
	t.Helper()
	select {
	case text, open := <-frames:
		if open {
			t.Fatalf("%s: unexpected frame %s", what, text)
		}
		t.Fatalf("%s: the stream ended unexpectedly", what)
	case <-time.After(300 * time.Millisecond):
	}
}

func expectClosed(t *testing.T, frames <-chan string, what string) {
	t.Helper()
	select {
	case text, open := <-frames:
		if open {
			t.Fatalf("%s: frame %s arrived after the source closed", what, text)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: the stream stayed open", what)
	}
}
