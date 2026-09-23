package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/newo-ether/filo/internal/desktop"
	"github.com/newo-ether/filo/internal/protocol"
)

type historyFixture struct {
	list  func(context.Context, string) (protocol.SessionPage, error)
	read  func(context.Context, string, string, bool, []string) (protocol.ConversationPage, error)
	reads atomic.Int32
}

func (h *historyFixture) List(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	if h.list != nil {
		return h.list(ctx, cursor)
	}
	return protocol.SessionPage{Sessions: []protocol.Session{}}, nil
}
func (h *historyFixture) Read(ctx context.Context, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
	h.reads.Add(1)
	if h.read != nil {
		return h.read(ctx, id, cursor, activity, excluded)
	}
	return protocol.ConversationPage{Messages: []protocol.Message{}, Queued: []protocol.QueuedInput{}}, nil
}

type readIPC struct {
	*ipcFixture
	connectErr  error
	snapshot    func(string) desktop.Message
	discoveries atomic.Int32
}

func (ipc *readIPC) Connect(context.Context) error { return ipc.connectErr }
func (ipc *readIPC) Request(ctx context.Context, method string, version int, params any, target string) (desktop.Message, error) {
	ipc.discoveries.Add(1)
	return ipc.ipcFixture.Request(ctx, method, version, params, target)
}
func (ipc *readIPC) Broadcast(method string, version int, params any, targets []string) error {
	if err := ipc.ipcFixture.Broadcast(method, version, params, targets); err != nil {
		return err
	}
	if fields(params)["following"] == true && ipc.snapshot != nil {
		go ipc.f.Receive(ipc.snapshot(text(fields(params)["conversationId"])))
	}
	return nil
}
func activeSnapshot(id string) desktop.Message {
	message := snapshotMessage(id, "original", 1)
	state := fields(fields(fields(message["params"])["change"])["conversationState"])
	state["latestModel"] = "gpt-test"
	state["hasUnreadTurn"] = true
	state["turns"] = []any{
		map[string]any{"turnId": "complete", "status": "completed", "items": []any{}},
		map[string]any{"turnId": "live", "status": "inProgress", "turnStartedAtMs": float64(2000), "items": []any{
			map[string]any{"id": "answer", "type": "agentMessage", "text": "stream"},
		}},
	}
	return message
}
func readFixture(h *historyFixture) (*DesktopSessions, *readIPC) {
	ipc := &readIPC{ipcFixture: &ipcFixture{owner: "original"}, snapshot: activeSnapshot}
	s := New(Options{IPC: ipc, History: h})
	ipc.f = s.f
	return s, ipc
}
func historical(id, turn string, timestamp protocol.Number) protocol.Message {
	return protocol.Message{MessageIdentity: protocol.MessageIdentity{ID: id, TurnID: turn, Timestamp: timestamp}, Text: protocol.Text(id)}
}

func TestListIncludesOrderedStatusesAndEmptyArrayWithoutHistoryReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &historyFixture{list: func(context.Context, string) (protocol.SessionPage, error) {
			return protocol.SessionPage{Sessions: []protocol.Session{{ID: "one"}, {ID: "two"}}}, nil
		}}
		s, ipc := readFixture(h)
		defer s.Close()
		release, err := s.Attach(context.Background(), "one")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		page, err := s.List(context.Background(), "")
		if err != nil || len(page.Statuses) != 2 {
			t.Fatal(page, err)
		}
		for i, id := range []string{"one", "two"} {
			v := page.Statuses[i]
			if v.ID != id || v.Status == nil || *v.Status != "active" || v.ActiveTurnID == nil || *v.ActiveTurnID != "live" || !v.HasUnreadTurn {
				t.Fatal(v)
			}
		}
		if h.reads.Load() != 0 || ipc.count(false) != 1 {
			t.Fatal("List loaded message bodies or released open chat")
		}
		h.list = nil
		empty, err := s.List(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(empty)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err = json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		if values, ok := wire["statuses"].([]any); !ok || len(values) != 0 {
			t.Fatal(string(body))
		}
		ipc.connectErr = errors.New("offline")
		h.list = func(context.Context, string) (protocol.SessionPage, error) {
			return protocol.SessionPage{Sessions: []protocol.Session{{ID: "one"}}}, nil
		}
		before := ipc.discoveries.Load()
		offline, err := s.List(context.Background(), "")
		if err != nil || offline.Statuses[0].Status != nil || ipc.discoveries.Load() != before {
			t.Fatal("Offline catalog was lost", err)
		}
	})
}

func TestReadMergesLiveTurnsCachesHistoryAndPreservesCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		next := "older"
		var cursorSeen string
		h := &historyFixture{read: func(_ context.Context, _ string, cursor string, _ bool, excluded []string) (protocol.ConversationPage, error) {
			cursorSeen = cursor
			if !reflect.DeepEqual(excluded, []string{"live"}) {
				t.Errorf("Excluded turns %v", excluded)
			}
			return protocol.ConversationPage{Messages: []protocol.Message{historical("old", "old-turn", 1), historical("stale", "live", 2)}, NextCursor: &next}, nil
		}}
		s, _ := readFixture(h)
		defer s.Close()
		for i := 0; i < 2; i++ {
			page, err := s.Read(context.Background(), "task", "", false, nil)
			if err != nil || len(page.Messages) != 2 || page.Messages[0].ID != "old" || page.Messages[1].ID != "answer" || page.NextCursor == nil || *page.NextCursor != "older" || page.Runtime.Status != "active" {
				t.Fatal(page, err)
			}
		}
		if h.reads.Load() != 1 {
			t.Fatal("Unchanged live reads reloaded history")
		}
		if _, err := s.Read(context.Background(), "task", "older", false, nil); err != nil || cursorSeen != "older" || h.reads.Load() != 2 {
			t.Fatal(cursorSeen, err)
		}
	})
}

func TestReadLeaseSurvivesConcurrentStatusReleaseAndSharedCallerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		h := &historyFixture{read: func(ctx context.Context, _ string, _ string, _ bool, _ []string) (protocol.ConversationPage, error) {
			select {
			case <-gate:
				return protocol.ConversationPage{}, nil
			case <-ctx.Done():
				return protocol.ConversationPage{}, ctx.Err()
			}
		}}
		s, ipc := readFixture(h)
		defer s.Close()
		ctx, cancel := context.WithCancel(context.Background())
		first, second := make(chan error, 1), make(chan error, 1)
		go func() { _, err := s.Read(ctx, "task", "", false, nil); first <- err }()
		go func() { _, err := s.Read(context.Background(), "task", "", false, nil); second <- err }()
		synctest.Wait()
		if values, err := s.Statuses(context.Background(), []string{"task"}); err != nil || values[0].Status == nil {
			t.Fatal(values, err)
		}
		if ipc.count(false) != 0 || h.reads.Load() != 1 {
			t.Fatal("Status disrupted a shared read")
		}
		cancel()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		close(gate)
		if err := <-second; err != nil {
			t.Fatal("A cancelled caller cancelled shared history", err)
		}
	})
}

func TestChangedReadModeDoesNotCancelAnotherModeOrCreateUnboundedReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		var running, maximum atomic.Int32
		h := &historyFixture{read: func(ctx context.Context, _ string, _ string, activity bool, _ []string) (protocol.ConversationPage, error) {
			n := running.Add(1)
			if n > maximum.Load() {
				maximum.Store(n)
			}
			defer running.Add(-1)
			if !activity {
				select {
				case <-gate:
				case <-ctx.Done():
					return protocol.ConversationPage{}, ctx.Err()
				}
			}
			return protocol.ConversationPage{}, nil
		}}
		s, _ := readFixture(h)
		defer s.Close()
		results := make(chan error, 2)
		go func() { _, err := s.Read(context.Background(), "task", "", false, nil); results <- err }()
		synctest.Wait()
		go func() { _, err := s.Read(context.Background(), "task", "", true, nil); results <- err }()
		synctest.Wait()
		close(gate)
		for i := 0; i < 2; i++ {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		if maximum.Load() != 1 || h.reads.Load() != 2 {
			t.Fatal("Read concurrency was not bounded", maximum.Load(), h.reads.Load())
		}
	})
}

func TestOnlySnapshotAbsenceFallsBackAndDoesNotMutateCachedRuntime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		model, turn := "known-model", "old-turn"
		runtime := &protocol.Runtime{Status: "active", ActiveTurnID: &turn, Model: &model}
		h := &historyFixture{read: func(context.Context, string, string, bool, []string) (protocol.ConversationPage, error) {
			return protocol.ConversationPage{Runtime: runtime}, nil
		}}
		s, ipc := readFixture(h)
		defer s.Close()
		ipc.snapshot = nil
		start := time.Now()
		page, err := s.Read(context.Background(), "task", "", false, nil)
		if err != nil || time.Since(start) != 2*time.Second || page.Runtime.Status != "notLoaded" || page.Runtime.ActiveTurnID != nil || *page.Runtime.Model != model || runtime.Status != "active" {
			t.Fatal(page, err)
		}
		ipc.mu.Lock()
		ipc.err = errors.New("transport failed")
		ipc.mu.Unlock()
		if _, err = s.Read(context.Background(), "task", "", false, nil); err == nil || h.reads.Load() != 1 {
			t.Fatal("Transport failure became historical success")
		}
	})
}

func TestReadFailureRetriesAndCloseCancelsOnlyOwnedPendingRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fail := true
		h := &historyFixture{read: func(ctx context.Context, _ string, _ string, _ bool, _ []string) (protocol.ConversationPage, error) {
			if fail {
				return protocol.ConversationPage{}, errors.New("read failed")
			}
			<-ctx.Done()
			return protocol.ConversationPage{}, ctx.Err()
		}}
		s, _ := readFixture(h)
		if _, err := s.Read(context.Background(), "task", "", false, nil); err == nil {
			t.Fatal("Read error hidden")
		}
		fail = false
		result := make(chan error, 1)
		go func() { _, err := s.Read(context.Background(), "task", "", false, nil); result <- err }()
		synctest.Wait()
		s.Close()
		if err := <-result; err == nil || h.reads.Load() != 2 {
			t.Fatal("Failed cache retained or close leaked reader", err)
		}
	})
}

func TestConcurrentListRowsRetainEveryReaderAndLeaveChatSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &historyFixture{list: func(context.Context, string) (protocol.SessionPage, error) {
			return protocol.SessionPage{Sessions: []protocol.Session{{ID: "a"}, {ID: "b"}, {ID: "c"}}}, nil
		}}
		s, ipc := readFixture(h)
		defer s.Close()
		release, err := s.Attach(context.Background(), "chat")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		var workers sync.WaitGroup
		errs := make(chan error, 4)
		for i := 0; i < 4; i++ {
			workers.Go(func() {
				page, err := s.List(context.Background(), "")
				if err == nil {
					for _, state := range page.Statuses {
						if state.Status == nil {
							err = errors.New("Shared list reader lost its snapshot")
						}
					}
				}
				errs <- err
			})
		}
		workers.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		ipc.f.mu.Lock()
		entry := ipc.f.entries["chat"]
		ipc.f.mu.Unlock()
		if entry == nil {
			t.Fatal("List evicted active chat")
		}
	})
}
