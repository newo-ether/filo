package sessions

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/desktop"
	"github.com/newo-ether/filo/internal/protocol"
)

// controlRequest is one native request this fixture answered.
type controlRequest struct {
	method  string
	version int
	params  map[string]any
	target  string
}

// controlIPC answers discovery and every follower control exactly like the TS
// desktop fixture: a start reports a new turn, a steer reports the steered turn,
// and an interrupt echoes the expected turn it was given.
type controlIPC struct {
	*ipcFixture

	stateMu    sync.Mutex
	requests   []controlRequest
	state      map[string]any
	dispatch   error
	connectErr error
}

func (ipc *controlIPC) Connect(context.Context) error { return ipc.connectErr }

func (ipc *controlIPC) Request(ctx context.Context, method string, version int, params any, target string) (desktop.Message, error) {
	ipc.stateMu.Lock()
	ipc.requests = append(ipc.requests, controlRequest{method, version, fields(params), target})
	dispatch := ipc.dispatch
	ipc.stateMu.Unlock()
	if method == "thread-owner-discovery" {
		return ipc.ipcFixture.Request(ctx, method, version, params, target)
	}
	if dispatch != nil {
		return nil, dispatch
	}
	switch method {
	case "thread-follower-start-turn":
		return desktop.Message{"resultType": "success", "handledByClientId": target,
			"result": map[string]any{"result": map[string]any{"turn": map[string]any{"id": "new-turn"}}}}, nil
	case "thread-follower-steer-turn":
		return desktop.Message{"resultType": "success", "handledByClientId": target,
			"result": map[string]any{"result": map[string]any{"turnId": "turn"}}}, nil
	}
	return desktop.Message{"resultType": "success", "handledByClientId": target,
		"result": map[string]any{"interruptedTurnId": fields(params)["expectedTurnId"], "ok": true}}, nil
}

func (ipc *controlIPC) Broadcast(method string, version int, params any, targets []string) error {
	if err := ipc.ipcFixture.Broadcast(method, version, params, targets); err != nil {
		return err
	}
	if fields(params)["following"] != true {
		return nil
	}
	ipc.stateMu.Lock()
	state := ipc.state
	ipc.stateMu.Unlock()
	ipc.ipcFixture.mu.Lock()
	owner := ipc.ipcFixture.owner
	ipc.ipcFixture.mu.Unlock()
	go ipc.f.Receive(snapshotOf(text(fields(params)["conversationId"]), owner, 1, state))
	return nil
}

func (ipc *controlIPC) setOwner(owner string) {
	ipc.ipcFixture.mu.Lock()
	ipc.ipcFixture.owner = owner
	ipc.ipcFixture.mu.Unlock()
}

func (ipc *controlIPC) setDispatch(err error) {
	ipc.stateMu.Lock()
	ipc.dispatch = err
	ipc.stateMu.Unlock()
}

// operations drops the discovery calls every control performs first.
func (ipc *controlIPC) operations() []controlRequest {
	ipc.stateMu.Lock()
	defer ipc.stateMu.Unlock()
	var operations []controlRequest
	for _, request := range ipc.requests {
		if request.method != "thread-owner-discovery" {
			operations = append(operations, request)
		}
	}
	return operations
}

func (ipc *controlIPC) broadcasts() []broadcast {
	ipc.ipcFixture.mu.Lock()
	defer ipc.ipcFixture.mu.Unlock()
	return append([]broadcast(nil), ipc.ipcFixture.calls...)
}

func (ipc *controlIPC) clear() {
	ipc.stateMu.Lock()
	ipc.requests = nil
	ipc.stateMu.Unlock()
	ipc.ipcFixture.mu.Lock()
	ipc.ipcFixture.calls = nil
	ipc.ipcFixture.mu.Unlock()
}

func snapshotOf(id, owner string, revision float64, state map[string]any) desktop.Message {
	return desktop.Message{"method": "thread-stream-state-changed", "version": float64(11), "sourceClientId": owner,
		"params": map[string]any{"hostId": "local", "conversationId": id,
			"change": map[string]any{"type": "snapshot", "revision": revision, "conversationState": state}}}
}

// desktopState builds one confirmed native snapshot whose last turn and runtime
// status are set independently, which is how an unfinished historical turn in an
// idle desktop is expressed.
func desktopState(id, turnStatus, runtimeStatus string) map[string]any {
	return map[string]any{
		"id": id, "hostId": "local", "cwd": "C:/project",
		"threadRuntimeStatus": map[string]any{"type": runtimeStatus},
		"turns": []any{map[string]any{
			"turnId": "turn", "status": turnStatus, "turnStartedAtMs": float64(1000),
			"items": []any{map[string]any{"type": "userMessage", "id": "native-user",
				"content": []any{map[string]any{"type": "text", "text": "original"}}}},
		}},
	}
}

// baseFixture is the peripheral native catalog and the only archive authority.
type baseFixture struct {
	mu          sync.Mutex
	models      []protocol.Model
	archive     func(string) (string, error)
	modelReads  int
	archiveSeen []string
}

var _ Base = (*baseFixture)(nil)

func (b *baseFixture) Create(context.Context, string, string, protocol.SessionSettings) (protocol.Session, protocol.SendReceipt, error) {
	return protocol.Session{}, protocol.SendReceipt{}, errors.New("unused catalog operation")
}
func (b *baseFixture) Models(context.Context) ([]protocol.Model, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.modelReads++
	return b.models, nil
}
func (b *baseFixture) Rename(context.Context, string, string) (protocol.Session, error) {
	return protocol.Session{}, errors.New("unused catalog operation")
}
func (b *baseFixture) Archive(_ context.Context, id string) (string, error) {
	b.mu.Lock()
	b.archiveSeen = append(b.archiveSeen, id)
	archive := b.archive
	b.mu.Unlock()
	if archive != nil {
		return archive(id)
	}
	return "", errors.New("unused catalog operation")
}
func (b *baseFixture) archives() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.archiveSeen...)
}

type controlSessions struct {
	sessions *DesktopSessions
	ipc      *controlIPC
	history  *historyFixture
}

func newControlSessions(state map[string]any, base Base, created CreatedReader) *controlSessions {
	ipc := &controlIPC{ipcFixture: &ipcFixture{owner: "original"}, state: state}
	history := &historyFixture{}
	sessions := New(Options{IPC: ipc, History: history, Base: base, Created: created})
	ipc.f = sessions.f
	return &controlSessions{sessions: sessions, ipc: ipc, history: history}
}

func TestSendStartsOneTurnOnIdleAndSteersTheActiveTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, active := range []bool{false, true} {
			state := desktopState("thread", "completed", "idle")
			if active {
				state = desktopState("thread", "inProgress", "active")
			}
			f := newControlSessions(state, nil, nil)
			defer f.sessions.Close()
			receipt, err := f.sessions.Send(context.Background(), "thread", "hello", "client")
			if err != nil {
				t.Fatal(err)
			}
			if receipt.ClientID != "client" {
				t.Fatal(receipt)
			}
			operations := f.ipc.operations()
			if len(operations) != 1 {
				t.Fatalf("Active %v issued %v", active, operations)
			}
			call := operations[0]
			if call.target != "original" {
				t.Fatal(call)
			}
			if !active {
				if call.method != "thread-follower-start-turn" || call.version != 2 {
					t.Fatal(call)
				}
				request := fields(fields(call.params["turnStart"])["request"])
				if text(request["threadId"]) != "thread" || text(request["clientUserMessageId"]) != "client" {
					t.Fatal(request)
				}
				if receipt.TurnID != "new-turn" {
					t.Fatal(receipt)
				}
				continue
			}
			if call.method != "thread-follower-steer-turn" || call.version != 1 || receipt.TurnID != "turn" {
				t.Fatal(call, receipt)
			}
			parts, isArray := call.params["input"].([]any)
			if !isArray || len(parts) != 1 || text(fields(parts[0])["text"]) != "hello" {
				t.Fatal(call.params)
			}
			restore := fields(call.params["restoreMessage"])
			if text(restore["clientUserMessageId"]) != "" && text(restore["text"]) != "hello" {
				t.Fatal(restore)
			}
			if text(restore["cwd"]) != "C:/project" {
				t.Fatal(restore)
			}
			context := fields(restore["context"])
			roots, isArray := context["workspaceRoots"].([]any)
			if !isArray || len(roots) != 1 || text(roots[0]) != "C:/project" {
				t.Fatal(context)
			}
		}
	})
}

func TestLostConfirmedOwnerNeverReachesHistoryOrTheCreatedScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		created := &createdFixture{owns: func(string) (bool, error) { return true, nil }}
		f := newControlSessions(desktopState("thread", "inProgress", "active"), nil, created)
		defer f.sessions.Close()
		release, err := f.sessions.Attach(context.Background(), "thread")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		f.ipc.setOwner("")
		if _, err := f.sessions.Send(context.Background(), "thread", "hello", "client"); err == nil ||
			!strings.Contains(err.Error(), "owner is unavailable") {
			t.Fatal(err)
		}
		if err := f.sessions.Stop(context.Background(), "thread", "turn"); err == nil ||
			!strings.Contains(err.Error(), "owner is unavailable") {
			t.Fatal(err)
		}
		if len(f.ipc.operations()) != 0 || len(created.entries()) != 0 {
			t.Fatal("A lost owner fell through to another execution path")
		}
	})
}

func TestNeverOwnedHistoryStaysReadableAndCannotAcquireExecution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		created := &createdFixture{owns: func(string) (bool, error) { return false, nil }}
		f := newControlSessions(desktopState("thread", "inProgress", "active"), nil, created)
		defer f.sessions.Close()
		f.ipc.setOwner("")
		release, err := f.sessions.Attach(context.Background(), "thread")
		if err != nil {
			t.Fatal(err)
		}
		release()
		if f.history.reads.Load() != 1 {
			t.Fatal("Browsing persisted history did not read it exactly once")
		}
		if _, err := f.sessions.Read(context.Background(), "thread", "", false, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := f.sessions.Send(context.Background(), "thread", "hello", "client"); err == nil {
			t.Fatal("Unconfirmed ownership authorized a start")
		}
		if err := f.sessions.Stop(context.Background(), "thread", "turn"); err == nil {
			t.Fatal("Unconfirmed ownership authorized a stop")
		}
		if err := f.sessions.UpdateSettings(context.Background(), "thread",
			protocol.SessionSettings{Effort: protocol.Known("low")}); err == nil {
			t.Fatal("Unconfirmed ownership authorized a settings mutation")
		}
		if err := f.sessions.SetModel(context.Background(), "thread", "model"); err == nil {
			t.Fatal("Unconfirmed ownership authorized a model mutation")
		}
		if len(f.ipc.operations()) != 0 || len(f.ipc.broadcasts()) != 0 || len(created.entries()) != 0 {
			t.Fatal("An ownerless task acquired an execution path")
		}
	})
}

func TestStopRequiresTheExactActiveTurnOfTheOriginalOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newControlSessions(desktopState("thread", "inProgress", "active"), nil, nil)
		defer f.sessions.Close()
		if err := f.sessions.Stop(context.Background(), "thread", "stale"); err == nil ||
			!strings.Contains(err.Error(), "unavailable") {
			t.Fatal("A stale turn was stopped", err)
		}
		if err := f.sessions.Stop(context.Background(), "thread", "turn"); err != nil {
			t.Fatal(err)
		}
		operations := f.ipc.operations()
		if len(operations) != 1 || operations[0].method != "thread-follower-interrupt-turn" ||
			operations[0].version != 4 || operations[0].target != "original" ||
			text(operations[0].params["expectedTurnId"]) != "turn" {
			t.Fatal(operations)
		}
	})
}

func TestIdleDesktopRejectsAStaleStopAndStartsNewInputOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// An unfinished historical turn with an idle native runtime is not active.
		f := newControlSessions(desktopState("thread", "inProgress", "idle"), nil, nil)
		defer f.sessions.Close()
		f.history.list = func(context.Context, string) (protocol.SessionPage, error) {
			return protocol.SessionPage{Sessions: []protocol.Session{{ID: "thread"}}}, nil
		}
		page, err := f.sessions.List(context.Background(), "")
		if err != nil || len(page.Statuses) != 1 || page.Statuses[0].ActiveTurnID != nil {
			t.Fatal(page, err)
		}
		if err := f.sessions.Stop(context.Background(), "thread", "turn"); err == nil {
			t.Fatal("An idle desktop stopped a historical turn")
		}
		if _, err := f.sessions.Send(context.Background(), "thread", "follow up", "client"); err != nil {
			t.Fatal(err)
		}
		operations := f.ipc.operations()
		if len(operations) != 1 || operations[0].method != "thread-follower-start-turn" ||
			operations[0].target != "original" {
			t.Fatal(operations)
		}
	})
}

func TestSettingsValidateNativeCapabilitiesAndNeverAuthorizeFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tier := protocol.ServiceTier{ID: "priority"}
		base := &baseFixture{models: []protocol.Model{{ID: "model", IsDefault: true,
			ReasoningEfforts: []string{"low", "ultra"}, ServiceTiers: []protocol.ServiceTier{tier}}}}
		state := desktopState("thread", "inProgress", "active")
		state["latestModel"] = "model"
		state["latestReasoningEffort"] = "ultra"
		state["latestThreadSettings"] = map[string]any{"effort": "ultra", "serviceTier": nil}
		f := newControlSessions(state, base, nil)
		defer f.sessions.Close()
		page, err := f.sessions.Read(context.Background(), "thread", "", false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if page.Runtime == nil || !page.Runtime.Effort.Known || page.Runtime.Effort.Value == nil ||
			*page.Runtime.Effort.Value != "ultra" {
			t.Fatal(page.Runtime)
		}
		if !page.Runtime.ServiceTier.Known || page.Runtime.ServiceTier.Value != nil {
			t.Fatal("A null service tier is the native default")
		}
		if err := f.sessions.UpdateSettings(context.Background(), "thread",
			protocol.SessionSettings{Effort: protocol.Known("none")}); err == nil ||
			!strings.Contains(err.Error(), "Thinking level is unavailable") {
			t.Fatal("An unsupported thinking level was accepted", err)
		}
		if err := f.sessions.UpdateSettings(context.Background(), "thread", protocol.SessionSettings{
			Effort: protocol.Known("low"), ServiceTier: protocol.Known(&tier.ID)}); err != nil {
			t.Fatal(err)
		}
		operations := f.ipc.operations()
		if len(operations) != 1 || operations[0].method != "thread-follower-update-thread-settings" ||
			operations[0].version != 1 || operations[0].target != "original" {
			t.Fatal(operations)
		}
		settings, isSettings := operations[0].params["threadSettings"].(protocol.SessionSettings)
		if !isSettings || !settings.Effort.Known || settings.Effort.Value != "low" ||
			!settings.ServiceTier.Known || settings.ServiceTier.Value == nil ||
			*settings.ServiceTier.Value != "priority" {
			t.Fatal(operations[0].params)
		}
		again, err := f.sessions.Read(context.Background(), "thread", "", false, nil)
		if err != nil || again.Runtime.Effort.Value == nil || *again.Runtime.Effort.Value != "ultra" {
			t.Fatal("An acknowledgement fabricated a readback", again.Runtime)
		}
		f.ipc.setOwner("")
		if err := f.sessions.UpdateSettings(context.Background(), "thread",
			protocol.SessionSettings{Effort: protocol.Known("low")}); err == nil ||
			!strings.Contains(err.Error(), "unavailable") {
			t.Fatal("A lost owner authorized a settings mutation", err)
		}
		if len(f.ipc.operations()) != 1 {
			t.Fatal("A lost owner dispatched a second settings mutation")
		}
	})
}

func TestArchivePublishesOnlyAfterTheNativeCallSucceeded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, turnStatus := range []string{"completed", "inProgress"} {
			runtimeStatus := "idle"
			if turnStatus == "inProgress" {
				runtimeStatus = "active"
			}
			base := &baseFixture{}
			f := newControlSessions(desktopState("thread", turnStatus, runtimeStatus), base, nil)
			defer f.sessions.Close()
			started, unblock := make(chan struct{}), make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(unblock) }) }
			base.archive = func(string) (string, error) {
				close(started)
				<-unblock
				return "C:/project", nil
			}
			attached, err := f.sessions.Attach(context.Background(), "thread")
			if err != nil {
				t.Fatal(err)
			}
			attached()
			f.ipc.clear()
			result := make(chan error, 1)
			go func() {
				_, err := f.sessions.Archive(context.Background(), "thread")
				result <- err
			}()
			<-started
			if len(f.ipc.broadcasts()) != 0 {
				finish()
				<-result
				t.Fatal("An archive published before the native call succeeded")
			}
			finish()
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			broadcasts := f.ipc.broadcasts()
			if len(broadcasts) != 2 || broadcasts[0].method != "thread-archived" || broadcasts[0].version != 2 ||
				broadcasts[0].targets != nil {
				t.Fatal(broadcasts)
			}
			if broadcasts[1].method != "thread-stream-following-changed" || broadcasts[1].params["following"] != false ||
				text(broadcasts[1].params["conversationId"]) != "thread" {
				t.Fatal(broadcasts[1])
			}
			params := broadcasts[0].params
			if text(params["hostId"]) != "local" || text(params["conversationId"]) != "thread" ||
				text(params["cwd"]) != "C:/project" {
				t.Fatal(params)
			}
			if len(f.ipc.operations()) != 0 {
				t.Fatal("Archive checked ownership or sent a stop")
			}
		}
	})
}

func TestArchiveFailureNeverBroadcastsAndADisconnectedDesktopBlocksDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &baseFixture{}
		base.archive = func(string) (string, error) { return "", errors.New("native archive failed") }
		f := newControlSessions(desktopState("thread", "completed", "idle"), base, nil)
		defer f.sessions.Close()
		if _, err := f.sessions.Archive(context.Background(), "thread"); err == nil ||
			!strings.Contains(err.Error(), "native archive failed") {
			t.Fatal(err)
		}
		if len(f.ipc.broadcasts()) != 0 {
			t.Fatal("A failed archive was published")
		}
		f.ipc.connectErr = errors.New("desktop disconnected")
		if _, err := f.sessions.Archive(context.Background(), "thread"); err == nil ||
			!strings.Contains(err.Error(), "desktop disconnected") {
			t.Fatal(err)
		}
		if archives := base.archives(); !reflect.DeepEqual(archives, []string{"thread"}) {
			t.Fatal("A failed archive was retried", archives)
		}
		if len(f.ipc.broadcasts()) != 0 {
			t.Fatal("A disconnected desktop published an archive")
		}
	})
}

func TestOnlyPositivelyCreatedOwnerlessTasksUseScopedControls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		created := &createdFixture{owns: func(id string) (bool, error) { return id == "created", nil }}
		f := newControlSessions(desktopState("created", "completed", "idle"), nil, created)
		defer f.sessions.Close()
		f.ipc.setOwner("")
		release, err := f.sessions.Attach(context.Background(), "created")
		if err != nil {
			t.Fatal(err)
		}
		release()
		page, err := f.sessions.Read(context.Background(), "created", "before", true, nil)
		if err != nil || len(page.Messages) != 1 {
			t.Fatal(page, err)
		}
		if _, err := f.sessions.Send(context.Background(), "created", "hello", "client"); err != nil {
			t.Fatal(err)
		}
		if err := f.sessions.UpdateSettings(context.Background(), "created", protocol.SessionSettings{
			Effort: protocol.Known("high"), ServiceTier: protocol.Known(strPtr("priority"))}); err != nil {
			t.Fatal(err)
		}
		if err := f.sessions.Stop(context.Background(), "created", "turn"); err != nil {
			t.Fatal(err)
		}
		want := []string{"attach created", "read created before true", "send created hello client",
			"settings created effort=high serviceTier=priority", "stop created turn"}
		if entries := created.entries(); !reflect.DeepEqual(entries, want) {
			t.Fatal(entries)
		}
		reads := f.history.reads.Load()
		if _, err := f.sessions.Send(context.Background(), "ordinary", "hello", "client"); err == nil ||
			!strings.Contains(err.Error(), "owner is unavailable") {
			t.Fatal("An ordinary ownerless task used the created scope", err)
		}
		if _, err := f.sessions.Read(context.Background(), "ordinary", "", false, nil); err != nil ||
			f.history.reads.Load() != reads+1 {
			t.Fatal("Ordinary history was not readable", err)
		}
	})
}

func TestOriginalOwnerAndAnUncertainMutationNeverUseTheCreatedScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		created := &createdFixture{owns: func(string) (bool, error) { return true, nil }}
		f := newControlSessions(desktopState("created", "inProgress", "active"), nil, created)
		defer f.sessions.Close()
		receipt, err := f.sessions.Send(context.Background(), "created", "hello", "client")
		if err != nil || receipt.TurnID != "turn" {
			t.Fatal(receipt, err)
		}
		f.ipc.setDispatch(errors.New("Outcome unknown"))
		if _, err := f.sessions.Send(context.Background(), "created", "again", "second"); err == nil ||
			!strings.Contains(err.Error(), "Outcome unknown") {
			t.Fatal(err)
		}
		f.ipc.setOwner("")
		if _, err := f.sessions.Send(context.Background(), "created", "again", "third"); err == nil ||
			!strings.Contains(err.Error(), "owner is unavailable") {
			t.Fatal(err)
		}
		if entries := created.entries(); len(entries) != 0 {
			t.Fatal("The created scope dispatched a confirmed desktop task", entries)
		}
	})
}

func TestDiscoveryFailureAndAnOwnerAppearingDuringDispatchPreventAuxiliaryExecution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failing := newControlSessions(desktopState("created", "completed", "idle"), nil,
			&createdFixture{owns: func(string) (bool, error) { return true, nil }})
		defer failing.sessions.Close()
		failing.ipc.ipcFixture.mu.Lock()
		failing.ipc.ipcFixture.err = errors.New("Discovery failed")
		failing.ipc.ipcFixture.mu.Unlock()
		if _, err := failing.sessions.Send(context.Background(), "created", "hello", "client"); err == nil ||
			!strings.Contains(err.Error(), "Discovery failed") {
			t.Fatal(err)
		}

		appearing := &createdFixture{}
		racing := newControlSessions(desktopState("created", "completed", "idle"), nil, appearing)
		defer racing.sessions.Close()
		racing.ipc.setOwner("")
		appearing.owns = func(string) (bool, error) {
			racing.ipc.setOwner("original")
			return true, nil
		}
		if _, err := racing.sessions.Send(context.Background(), "created", "hello", "client"); err == nil ||
			!strings.Contains(err.Error(), "owner is unavailable") {
			t.Fatal(err)
		}
		if entries := appearing.entries(); len(entries) != 0 {
			t.Fatal("An owner appearing mid-dispatch still used the created scope", entries)
		}
	})
}

func TestCatalogReportsScopedStatesWithoutInventingOrdinaryRows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		created := &createdFixture{owns: func(id string) (bool, error) { return id == "created", nil }}
		created.statuses = func([]string) ([]protocol.SessionStatus, error) {
			active, activeTurn := "active", "turn"
			return []protocol.SessionStatus{{ID: "created", Status: &active, ActiveTurnID: &activeTurn}}, nil
		}
		f := newControlSessions(desktopState("created", "completed", "idle"), nil, created)
		defer f.sessions.Close()
		f.ipc.setOwner("")
		f.history.list = func(context.Context, string) (protocol.SessionPage, error) {
			return protocol.SessionPage{Sessions: []protocol.Session{{ID: "created"}, {ID: "ordinary"}}}, nil
		}
		page, err := f.sessions.List(context.Background(), "")
		if err != nil || len(page.Statuses) != 2 {
			t.Fatal(page, err)
		}
		if page.Statuses[0].ActiveTurnID == nil || *page.Statuses[0].ActiveTurnID != "turn" ||
			page.Statuses[1].Status != nil {
			t.Fatal(page.Statuses)
		}
		want := []string{"statuses created,ordinary"}
		if entries := created.entries(); !reflect.DeepEqual(entries, want) {
			t.Fatal(entries)
		}
		created.statuses = func([]string) ([]protocol.SessionStatus, error) {
			return nil, errors.New("Helper offline")
		}
		broken, err := f.sessions.List(context.Background(), "")
		if err != nil || len(broken.Statuses) != 2 || broken.Statuses[0].Status != nil || broken.Statuses[1].Status != nil {
			t.Fatal("A helper failure lost the native catalog", broken, err)
		}
	})
}

func strPtr(value string) *string { return &value }
