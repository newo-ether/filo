package service

import (
	"context"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestScopedTurnsRetainConfirmedPriorityTier(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	ctx := context.Background()
	tier := "priority"
	settings := protocol.SessionSettings{ServiceTier: protocol.Known(&tier)}
	if _, _, err := fixture.sessions.Create(ctx, "hello", "first", settings); err != nil {
		t.Fatal(err)
	}
	fixture.peer.finish()
	fixture.settle(t)
	if _, err := fixture.sessions.Send(ctx, taskSessionsTaskID, "again", "second"); err != nil {
		t.Fatal(err)
	}
	starts := fixture.peer.callsFor("turn/start")
	if len(starts) != 2 {
		t.Fatalf("native turn count = %d", len(starts))
	}
	for _, call := range starts {
		if call.params["serviceTier"] != "priority" {
			t.Fatalf("confirmed priority lost at turn start: %#v", call.params)
		}
	}
}
