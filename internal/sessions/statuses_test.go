package sessions

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestOwnerlessListedAndSeenStatusesRetainExactCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &historyFixture{}
		s, ipc := readFixture(h)
		defer s.Close()
		ipc.owner = ""
		s.f.mu.Lock()
		s.f.seen["seen"] = true
		s.f.mu.Unlock()
		h.list = func(context.Context, string) (protocol.SessionPage, error) {
			return protocol.SessionPage{Sessions: []protocol.Session{{ID: "seen"}, {ID: "listed"}, {ID: "newer-start"}}}, nil
		}
		h.read = func(_ context.Context, id, _ string, _ bool, _ []string) (protocol.ConversationPage, error) {
			turn := id + "-terminal"
			runtime := &protocol.Runtime{Status: "idle", CompletedTurnID: protocol.Known(&turn)}
			if id == "newer-start" {
				runtime.Status = "notLoaded"
				runtime.CompletedTurnID = protocol.Known[*string](nil)
			}
			return protocol.ConversationPage{Runtime: runtime}, nil
		}
		page, err := s.List(context.Background(), "")
		if err != nil || len(page.Statuses) != 3 {
			t.Fatal(page, err)
		}
		for index, id := range []string{"seen", "listed"} {
			status := page.Statuses[index]
			if status.ID != id || status.Status == nil || *status.Status != "idle" || status.ActiveTurnID != nil || status.CompletedTurnID == nil || *status.CompletedTurnID != id+"-terminal" {
				t.Fatal("terminal identity lost or status invented", status)
			}
		}
		if page.Statuses[2].CompletedTurnID != nil {
			t.Fatal("newer start exposed older completion")
		}
		if ipc.count(true) != 0 {
			t.Fatal("status browsing started follower")
		}
	})
}

func TestOwnerlessCompletionStatusKeepsFanoutBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &historyFixture{}
		s, ipc := readFixture(h)
		defer s.Close()
		ipc.owner = ""
		var active, maximum atomic.Int32
		gate := make(chan struct{})
		h.read = func(_ context.Context, id, _ string, _ bool, _ []string) (protocol.ConversationPage, error) {
			current := active.Add(1)
			defer active.Add(-1)
			for before := maximum.Load(); current > before && !maximum.CompareAndSwap(before, current); before = maximum.Load() {
			}
			<-gate
			return protocol.ConversationPage{}, nil
		}
		ids := make([]string, 12)
		for i := range ids {
			ids[i] = fmt.Sprint(i)
		}
		done := make(chan error, 1)
		go func() { _, err := s.Statuses(context.Background(), ids); done <- err }()
		synctest.Wait()
		if active.Load() != 3 {
			t.Fatal("status fanout changed", active.Load())
		}
		close(gate)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if maximum.Load() > 3 || h.reads.Load() != 12 {
			t.Fatal("unbounded or missing reads", maximum.Load(), h.reads.Load())
		}
	})
}
