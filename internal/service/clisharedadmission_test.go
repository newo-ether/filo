package service

import (
	"context"
	"strings"
	"testing"
)

func TestCLISharedAttachFailureNeverPublishesLiveEntry(t *testing.T) {
	surface, fixture := newCLIControlledFixture(t)
	fixture.peer.mu.Lock()
	fixture.peer.owned, fixture.peer.failAttach = true, true
	fixture.peer.mu.Unlock()
	// A prior admission supplies only a candidate ID. Native attach is still
	// required before the catalog can publish live access.
	fixture.sessions.mu.Lock()
	fixture.sessions.entries[taskSessionsTaskID] = &taskSessionsEntry{}
	fixture.sessions.mu.Unlock()
	ids, err := surface.liveOwnedIDs(context.Background())
	fixture.sessions.mu.Lock()
	delete(fixture.sessions.entries, taskSessionsTaskID)
	fixture.sessions.mu.Unlock()
	if err != nil || len(ids) != 1 {
		t.Fatalf("candidate IDs=%v err=%v", ids, err)
	}
	entry, err := fixture.sessions.acquire(context.Background(), ids[0], false)
	if entry != nil || err == nil {
		t.Fatalf("failed attach acquired entry: %v, %v", entry, err)
	}
	page, err := surface.List(context.Background(), "")
	if err != nil || len(page.Sessions) != 0 {
		t.Fatalf("failed attach appeared in catalog: %+v, %v", page, err)
	}
	if fixture.peer.callCount("thread/resume") != 1 {
		t.Fatal("catalog retried a failed native attach")
	}
	access, err := fixture.sessions.Access(context.Background(), taskSessionsTaskID)
	if err != nil || access.Capabilities.Live || access.Capabilities.IdleSend || access.Capabilities.Stop {
		t.Fatalf("failed attach retained live authority: %+v, %v", access, err)
	}
	fixture.sessions.mu.Lock()
	entry = fixture.sessions.entries[taskSessionsTaskID]
	fixture.sessions.mu.Unlock()
	if entry != nil {
		t.Fatal("failed attach was published")
	}
	for _, method := range []string{"thread/start", "turn/start", "turn/steer", "turn/interrupt"} {
		if fixture.peer.callCount(method) != 0 {
			t.Fatalf("failed attach dispatched %s", method)
		}
	}
}

func TestCLISharedAttachFailureReportsUnloadedAndClosesConnection(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	fixture.peer.mu.Lock()
	fixture.peer.owned, fixture.peer.failAttach = true, true
	fixture.peer.mu.Unlock()
	entry, err := fixture.sessions.acquire(context.Background(), taskSessionsTaskID, false)
	if entry != nil || err == nil || !strings.Contains(err.Error(), "may have unloaded") ||
		!strings.Contains(err.Error(), "Native thread unloaded before resume") {
		t.Fatalf("entry=%v err=%v", entry, err)
	}
	if _, err := fixture.rpc.Request("thread/read", map[string]any{"threadId": taskSessionsTaskID}); err == nil {
		t.Fatal("failed attach left its connection open")
	}
}
