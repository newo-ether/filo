package codex

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/nativejson"
)

type metadataCall struct {
	Method string
	Params any
}
type metadataFixture struct {
	mu                       sync.Mutex
	calls                    []metadataCall
	active, mismatch, failed bool
	entered, release         chan struct{}
	catalog                  any
}

func (f *metadataFixture) Request(method string, params any) (any, error) {
	f.mu.Lock()
	f.calls = append(f.calls, metadataCall{method, params})
	active, mismatch, failed, entered, release, catalog := f.active, f.mismatch, f.failed, f.entered, f.release, f.catalog
	f.mu.Unlock()
	if method == "thread/name/set" {
		if entered != nil {
			close(entered)
			<-release
		}
		if failed {
			return nil, errors.New("Native outcome unknown")
		}
	}
	if method == "thread/loaded/list" {
		return map[string]any{"data": []any{"task"}, "nextCursor": nil}, nil
	}
	if method == "thread/read" {
		id := params.(map[string]any)["threadId"]
		if mismatch {
			id = "other"
		}
		state := "idle"
		if active {
			state = "active"
		}
		return map[string]any{"thread": map[string]any{"id": id, "name": "Native name", "preview": "", "cwd": "native-workspace", "updatedAt": 7, "status": map[string]any{"type": state}}}, nil
	}
	if method == "model/list" {
		return catalog, nil
	}
	return map[string]any{}, nil
}

func (f *metadataFixture) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		names = append(names, call.Method)
	}
	return names
}

func TestMetadataPreservesNativeMutationOrderAndActiveArchive(t *testing.T) {
	f := &metadataFixture{}
	m := NewNativeMetadata(f)
	if _, err := m.Rename("task", "\ufeff \n"); err == nil {
		t.Fatal("Blank name accepted")
	}
	if len(f.methods()) != 0 {
		t.Fatal("Invalid rename contacted native host")
	}
	session, err := m.Rename("task", "New name")
	if err != nil || session.Title != "Native name" || session.UpdatedAt != 7 || session.Status.Value == nil || *session.Status.Value != "idle" {
		t.Fatalf("Native metadata changed: %+v %v", session, err)
	}
	f.active = true
	if cwd, err := m.Archive("task"); err != nil || cwd != "native-workspace" {
		t.Fatalf("Active archive rejected: %q %v", cwd, err)
	}
	if want := []string{"thread/name/set", "thread/read", "thread/read", "thread/archive"}; !reflect.DeepEqual(f.methods(), want) {
		t.Fatal(f.methods())
	}
	encoded, _ := json.Marshal(f.calls)
	const want = `[{"Method":"thread/name/set","Params":{"name":"New name","threadId":"task"}},{"Method":"thread/read","Params":{"threadId":"task"}},{"Method":"thread/read","Params":{"includeTurns":false,"threadId":"task"}},{"Method":"thread/archive","Params":{"threadId":"task"}}]`
	if string(encoded) != want {
		t.Fatal(string(encoded))
	}
}

func TestMetadataNeverReplaysUncertainWritesOrArchivesMismatchedTargets(t *testing.T) {
	f := &metadataFixture{failed: true}
	m := NewNativeMetadata(f)
	if _, err := m.Rename("task", "name"); err == nil || !strings.Contains(err.Error(), "outcome unknown") {
		t.Fatal(err)
	}
	f.mismatch = true
	if _, err := m.Archive("task"); err == nil || !strings.Contains(err.Error(), "target is unavailable") {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.methods(), []string{"thread/name/set", "thread/read"}) {
		t.Fatal(f.methods())
	}
}

func TestMetadataShutdownFencesWritesAndChecksIdlenessWithoutStoppingTasks(t *testing.T) {
	f := &metadataFixture{entered: make(chan struct{}), release: make(chan struct{})}
	m := NewNativeMetadata(f)
	done := make(chan error, 1)
	go func() { _, err := m.Rename("task", "name"); done <- err }()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("Rename did not start")
	}
	if err := m.PrepareShutdown(); err == nil || !strings.Contains(err.Error(), "operation is still running") {
		t.Fatal(err)
	}
	close(f.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.active = true
	if err := m.PrepareShutdown(); err == nil || !strings.Contains(err.Error(), "task is active") {
		t.Fatal(err)
	}
	f.active = false
	if err := m.PrepareShutdown(); err != nil {
		t.Fatal(err)
	}
	before := len(f.methods())
	if _, err := m.Rename("task", "late"); err == nil {
		t.Fatal("Rename passed shutdown fence")
	}
	if _, err := m.Archive("task"); err == nil {
		t.Fatal("Archive passed shutdown fence")
	}
	if err := m.PrepareShutdown(); err == nil {
		t.Fatal("Repeated shutdown passed")
	}
	if len(f.methods()) != before {
		t.Fatal("Shutdown rejection contacted host")
	}
	for _, method := range f.methods() {
		if method == "thread/resume" || method == "thread/delete" || strings.HasPrefix(method, "turn/") {
			t.Fatal("Peripheral metadata controlled execution", method)
		}
	}
}

func TestMetadataModelsPreserveEmptyUnknownAndExplicitNativeDefaults(t *testing.T) {
	const catalog = `{"data":[{"model":"known","displayName":"Known","isDefault":true,"supportedReasoningEfforts":[{"reasoningEffort":"high"}],"defaultReasoningEffort":"high","serviceTiers":[{"id":"priority","name":"Priority","description":"Native tier"},{"id":"ultrafast","name":"Ultrafast","description":"Native ultra fast"}],"defaultServiceTier":null},{"model":"empty","displayName":"Empty","isDefault":false,"supportedReasoningEfforts":[],"defaultReasoningEffort":"","serviceTiers":[]},{"model":"unknown","displayName":"Unknown","isDefault":false}]}`
	value, _, err := nativejson.Read(strings.NewReader(catalog), false)
	if err != nil {
		t.Fatal(err)
	}
	m := NewNativeMetadata(&metadataFixture{catalog: value})
	models, err := m.Models()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	const want = `[{"id":"known","name":"Known","isDefault":true,"reasoningEfforts":["high"],"defaultReasoningEffort":"high","serviceTiers":[{"id":"priority","name":"Priority","description":"Native tier"},{"id":"ultrafast","name":"Ultrafast","description":"Native ultra fast"}],"defaultServiceTier":null},{"id":"empty","name":"Empty","isDefault":false,"reasoningEfforts":[],"defaultReasoningEffort":"","serviceTiers":[]},{"id":"unknown","name":"Unknown","isDefault":false}]`
	if string(encoded) != want {
		t.Fatalf("Model capability presence changed: %s", encoded)
	}
}

func TestMetadataTextAndTitleFallbackSurviveNativeJSON(t *testing.T) {
	for _, source := range []string{
		`{"id":"task","name":null,"preview":"fallback","cwd":"workspace","updatedAt":1}`,
		`{"id":"task","name":"\ud800","preview":"","cwd":"workspace","updatedAt":1}`,
		`{"id":"task","name":"","preview":"","cwd":"workspace","updatedAt":1}`,
	} {
		value, _, err := nativejson.Read(strings.NewReader(source), false)
		if err != nil {
			t.Fatal(err)
		}
		var thread metadataThread
		if err := decodeMetadata(value, &thread); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(thread.session())
		if err != nil {
			t.Fatal(err)
		}
		want := "fallback"
		if strings.Contains(source, `\ud800`) {
			want = `\ud800`
		} else if strings.Contains(source, `"preview":""`) {
			want = "task"
		}
		if !strings.Contains(string(encoded), `"title":"`+want+`"`) || !strings.Contains(string(encoded), `"status":null`) {
			t.Fatal(string(encoded))
		}
	}
}

func TestMetadataNameLimitCountsNativeUTF16Units(t *testing.T) {
	for _, unit := range []string{"x", "\xed\xa0\x80"} {
		f := &metadataFixture{}
		m := NewNativeMetadata(f)
		if _, err := m.Rename("task", strings.Repeat(unit, 4096)); err != nil {
			t.Fatal(err)
		}
		before := len(f.methods())
		if _, err := m.Rename("task", strings.Repeat(unit, 4097)); err == nil {
			t.Fatal("Overlong name accepted")
		}
		if len(f.methods()) != before {
			t.Fatal("Overlong rename contacted native host")
		}
	}
}
