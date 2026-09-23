package codex

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

// sessionCall is one recorded request of the fake native connection.
type sessionCall struct {
	method  string
	params  map[string]any
	timeout time.Duration
	bounded bool
}

// sessionHost is a native connection that answers from installed replies. An
// uninstalled method answers with an empty result, exactly like a native packet
// whose result carried nothing this session retains.
type sessionHost struct {
	mu      sync.Mutex
	calls   []sessionCall
	replies map[string]func(params map[string]any) (any, error)
}

func newSessionHost() *sessionHost {
	return &sessionHost{replies: map[string]func(map[string]any) (any, error){}}
}

func (h *sessionHost) Request(method string, params any) (any, error) {
	return h.call(method, params, 0, false)
}

func (h *sessionHost) RequestTimeout(method string, params any, timeout time.Duration) (any, error) {
	return h.call(method, params, timeout, true)
}

func (h *sessionHost) call(method string, params any, timeout time.Duration, bounded bool) (any, error) {
	fields, _ := nativejson.Fields(params)
	h.mu.Lock()
	h.calls = append(h.calls, sessionCall{method: method, params: fields, timeout: timeout, bounded: bounded})
	reply := h.replies[method]
	h.mu.Unlock()
	if reply == nil {
		return map[string]any{}, nil
	}
	return reply(fields)
}

func (h *sessionHost) methods() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	methods := make([]string, 0, len(h.calls))
	for _, call := range h.calls {
		methods = append(methods, call.method)
	}
	return methods
}

func (h *sessionHost) count(method string) int {
	count := 0
	for _, sent := range h.methods() {
		if sent == method {
			count++
		}
	}
	return count
}

func (h *sessionHost) last() sessionCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[len(h.calls)-1]
}

// sessionReply installs one constant native result.
func sessionReply(value any) func(params map[string]any) (any, error) {
	return func(map[string]any) (any, error) { return value, nil }
}

// sessionFailure installs one native failure.
func sessionFailure(err error) func(params map[string]any) (any, error) {
	return func(map[string]any) (any, error) { return nil, err }
}

// sessionText reads one optional text pointer for a test diagnostic.
func sessionText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestNativeTaskSessionCreationInheritsHostDefaultsAndSettingsNeverStartATurn(t *testing.T) {
	host := newSessionHost()
	host.replies["thread/start"] = sessionReply(map[string]any{"thread": map[string]any{
		"id": "new", "cwd": "/host", "updatedAt": 1.0, "preview": ""}})
	host.replies["model/list"] = sessionReply(map[string]any{"data": []any{map[string]any{
		"model": "model", "displayName": "Model", "isDefault": true}}})
	sessions := NewNativeTaskSession(host)
	session, err := sessions.Create()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	created := host.calls[0]
	if created.method != "thread/start" || len(created.params) != 0 || !created.bounded ||
		created.timeout != protocol.DefaultTaskRequestTimeouts.Create {
		t.Fatalf("the creation call = %+v", created)
	}
	if session.ID != "new" || session.Cwd != "/host" || session.Title != "new" || session.UpdatedAt != 1 {
		t.Fatalf("the created session = %+v", session)
	}
	if !session.Status.Known || session.Status.Value != nil {
		t.Fatalf("the created status = %+v", session.Status)
	}
	if err := sessions.UpdateSettings("new", protocol.SessionSettings{Model: protocol.Known("model")}); err != nil {
		t.Fatalf("updateSettings: %v", err)
	}
	last := host.last()
	if last.method != "thread/settings/update" || len(last.params) != 2 ||
		last.params["threadId"] != "new" || last.params["model"] != "model" {
		t.Fatalf("the settings call = %+v", last)
	}
	for _, method := range host.methods() {
		if method == "turn/start" || method == "thread/resume" {
			t.Fatalf("selecting a model sent %s", method)
		}
	}
	if err := sessions.UpdateSettings("new", protocol.SessionSettings{Model: protocol.Known("unknown")}); err == nil ||
		!strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("an unknown model was accepted: %v", err)
	}
}

func TestNativeTaskSessionAttachSharesOneResumeAndForgetsAFailedAttempt(t *testing.T) {
	host := newSessionHost()
	resumes := 0
	host.replies["thread/resume"] = func(map[string]any) (any, error) {
		resumes++
		if resumes == 1 {
			return nil, errors.New("Native task is not verified idle")
		}
		return map[string]any{"model": "resumed", "reasoningEffort": "low", "serviceTier": "fast"}, nil
	}
	sessions := NewNativeTaskSession(host)
	if err := sessions.Attach("task"); err == nil {
		t.Fatal("a failed resume was reported as attached")
	}
	if err := sessions.Attach("task"); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if err := sessions.Attach("task"); err != nil {
		t.Fatalf("the cached attachment failed: %v", err)
	}
	if count := host.count("thread/resume"); count != 2 {
		t.Fatalf("the resumes = %d", count)
	}
	if params := host.last().params; params["threadId"] != "task" || params["excludeTurns"] != true {
		t.Fatalf("the resume params = %+v", params)
	}
	settings := sessions.settings["task"]
	if settings.Model == nil || *settings.Model != "resumed" || settings.Effort == nil || *settings.Effort != "low" ||
		!settings.ServiceTier.Known || settings.ServiceTier.Value == nil || *settings.ServiceTier.Value != "fast" {
		t.Fatalf("the resumed settings = %+v", settings)
	}
}

func TestNativeTaskSessionSnapshotReportsTheNativeRuntime(t *testing.T) {
	host := newSessionHost()
	host.replies["thread/resume"] = sessionReply(map[string]any{})
	host.replies["thread/read"] = sessionReply(map[string]any{"thread": map[string]any{
		"id": "task", "model": "host-model", "reasoningEffort": "medium",
		"status": map[string]any{"type": "active"}}})
	host.replies["filo/task/state"] = sessionReply(map[string]any{
		"turn":    map[string]any{"id": "turn", "status": "inProgress"},
		"hasUser": true, "materialized": true})
	sessions := NewNativeTaskSession(host)
	snapshot, err := sessions.Snapshot("task")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	runtime := snapshot.Page.Runtime
	if runtime == nil || runtime.Status != "active" || runtime.ActiveTurnID == nil || *runtime.ActiveTurnID != "turn" {
		t.Fatalf("the active runtime = %+v", runtime)
	}
	if !runtime.CompletedTurnID.Known || runtime.CompletedTurnID.Value != nil {
		t.Fatalf("the completed turn = %+v", runtime.CompletedTurnID)
	}
	if runtime.Model == nil || *runtime.Model != "host-model" {
		t.Fatalf("the inherited model = %+v", runtime.Model)
	}
	if !runtime.Effort.Known || runtime.Effort.Value == nil || *runtime.Effort.Value != "medium" {
		t.Fatalf("the inherited effort = %+v", runtime.Effort)
	}
	if runtime.ServiceTier.Known || runtime.ServiceTierKnown.Known {
		t.Fatalf("an unattached service tier was reported: %+v", runtime)
	}
	if runtime.ContextTokens != nil || runtime.ContextWindow != nil {
		t.Fatalf("an unreported usage was invented: %+v", runtime)
	}
	if !runtime.ActiveTurnHasUserMessage.Known || !runtime.ActiveTurnHasUserMessage.Value {
		t.Fatalf("the user message flag = %+v", runtime.ActiveTurnHasUserMessage)
	}
	if !snapshot.Materialized || snapshot.TurnID == nil || *snapshot.TurnID != "turn" {
		t.Fatalf("the snapshot = %+v", snapshot)
	}
	if snapshot.Page.Messages == nil || snapshot.Page.Queued == nil || snapshot.Page.NextCursor != nil {
		t.Fatalf("the empty page = %+v", snapshot.Page)
	}
	if err := sessions.Attach("task"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	changed := []string{}
	sessions.SetChangedHandler(func(id string) { changed = append(changed, id) })
	sessions.Receive("thread/tokenUsage/updated", map[string]any{"threadId": "task",
		"tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 2048.0}, "modelContextWindow": 128000.0}})
	sessions.Receive("thread/settings/updated", map[string]any{"threadId": "task",
		"threadSettings": map[string]any{"model": "native-model", "effort": "high", "serviceTier": nil}})
	// Another task of the same executor is never observed.
	sessions.Receive("thread/tokenUsage/updated", map[string]any{"threadId": "other",
		"tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 1.0}}})
	snapshot, err = sessions.Snapshot("task")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	runtime = snapshot.Page.Runtime
	if runtime.Model == nil || *runtime.Model != "native-model" || runtime.Effort.Value == nil ||
		*runtime.Effort.Value != "high" {
		t.Fatalf("the native settings = model:%q effort:%q tier:%+v", sessionText(runtime.Model),
			sessionText(runtime.Effort.Value), runtime.ServiceTier)
	}
	if !runtime.ServiceTier.Known || runtime.ServiceTier.Value != nil || !runtime.ServiceTierKnown.Known ||
		!runtime.ServiceTierKnown.Value {
		t.Fatalf("the native default service tier = %+v", runtime)
	}
	if runtime.ContextTokens == nil || *runtime.ContextTokens != 2048 ||
		runtime.ContextWindow == nil || *runtime.ContextWindow != 128000 {
		t.Fatalf("the native usage = %+v", runtime)
	}
	if !reflect.DeepEqual(changed, []string{"task", "task"}) {
		t.Fatalf("the reported changes = %v", changed)
	}
	host.replies["thread/read"] = sessionReply(map[string]any{"thread": map[string]any{"id": "unrelated"}})
	if _, err := sessions.Snapshot("task"); err == nil || err.Error() != "Native task identity mismatch" {
		t.Fatalf("the mismatched identity = %v", err)
	}
	host.replies["thread/read"] = sessionReply(map[string]any{"thread": map[string]any{"id": "task"}})
	host.replies["filo/task/state"] = sessionReply(map[string]any{
		"turn": map[string]any{"id": "turn", "status": 7.0}, "hasUser": true, "materialized": true})
	if _, err := sessions.Snapshot("task"); err == nil || err.Error() != "Invalid executor task state" {
		t.Fatalf("an unusable task state = %v", err)
	}
}

func TestNativeTaskSessionForgetsAClosedThreadAndReportsConnectionLoss(t *testing.T) {
	host := newSessionHost()
	host.replies["thread/start"] = sessionReply(map[string]any{"thread": map[string]any{"id": "task"},
		"model": "host-model"})
	host.replies["thread/read"] = sessionReply(map[string]any{"thread": map[string]any{"id": "task", "model": "host-model"}})
	host.replies["filo/task/state"] = sessionReply(map[string]any{"hasUser": false, "materialized": true})
	sessions := NewNativeTaskSession(host)
	if _, err := sessions.Create(); err != nil {
		t.Fatalf("create: %v", err)
	}
	sessions.Receive("thread/tokenUsage/updated", map[string]any{"threadId": "task",
		"tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 2048.0}}})
	snapshot, err := sessions.Snapshot("task")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if tokens := snapshot.Page.Runtime.ContextTokens; tokens == nil || *tokens != 2048 {
		t.Fatalf("the reported usage = %+v", snapshot.Page.Runtime)
	}
	changed := []string{}
	closed := 0
	sessions.SetChangedHandler(func(id string) { changed = append(changed, id) })
	sessions.SetClosedHandler(func() { closed++ })
	sessions.Receive("thread/closed", map[string]any{"threadId": "task"})
	if !reflect.DeepEqual(changed, []string{"task"}) {
		t.Fatalf("the reported changes = %v", changed)
	}
	sessions.Receive("thread/tokenUsage/updated", map[string]any{"threadId": "task",
		"tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 4096.0}}})
	snapshot, err = sessions.Snapshot("task")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Page.Runtime.ContextTokens != nil {
		t.Fatalf("a detached task kept its live usage: %+v", snapshot.Page.Runtime)
	}
	sessions.Disconnected()
	if closed != 1 {
		t.Fatalf("the connection losses = %d", closed)
	}
	sessions.Receive("thread/settings/updated", map[string]any{"threadId": "task",
		"threadSettings": map[string]any{"model": "late"}})
	if !reflect.DeepEqual(changed, []string{"task"}) {
		t.Fatalf("a disconnected session observed %v", changed)
	}
}

func TestNativeTaskSessionArchiveForgetsTheOwnedTask(t *testing.T) {
	host := newSessionHost()
	host.replies["thread/read"] = sessionReply(map[string]any{"thread": map[string]any{"id": "task", "cwd": "C:\\work"}})
	host.replies["thread/archive"] = sessionReply(map[string]any{})
	host.replies["model/list"] = sessionReply(map[string]any{"data": []any{}})
	sessions := NewNativeTaskSession(host)
	if _, err := sessions.Models(); err != nil {
		t.Fatalf("models: %v", err)
	}
	changed := []string{}
	sessions.SetChangedHandler(func(id string) { changed = append(changed, id) })
	cwd, err := sessions.ArchiveSession("task")
	if err != nil {
		t.Fatalf("archiveSession: %v", err)
	}
	if cwd != "C:\\work" {
		t.Fatalf("the archived directory = %q", cwd)
	}
	if !reflect.DeepEqual(changed, []string{"task"}) {
		t.Fatalf("the reported changes = %v", changed)
	}
	for _, method := range host.methods() {
		if method == "turn/start" || method == "turn/interrupt" || method == "thread/resume" {
			t.Fatalf("archiving sent %s", method)
		}
	}
	if err := sessions.Stop("task", "turn"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if params := host.last().params; params["threadId"] != "task" || params["turnId"] != "turn" {
		t.Fatalf("the interrupt params = %+v", params)
	}
	host.replies["thread/read"] = sessionFailure(errors.New("Native archive target is unavailable"))
	if _, err := sessions.ArchiveSession("task"); err == nil {
		t.Fatal("an unavailable archive target was archived")
	}
}
