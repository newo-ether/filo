package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestTaskAccessCreateScopeChangeStopsAtEveryBoundary(t *testing.T) {
	for _, stage := range []string{"before-open", "after-thread-start", "after-models", "after-settings"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newTaskSessionsFixture(t)
			changed := errors.New("Fixture native scope changed")
			ctx := protocol.WithScopeValidation(context.Background(), func(ctx context.Context) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				stop := stage == "before-open"
				for _, call := range fixture.peer.calls() {
					stop = stop || stage == "after-thread-start" && call.method == "thread/start" ||
						stage == "after-models" && call.method == "model/list" ||
						stage == "after-settings" && call.method == "thread/settings/update"
				}
				if stop {
					return changed
				}
				return nil
			})
			settings := protocol.SessionSettings{Model: protocol.Known("m"), Effort: protocol.Known("high")}
			_, _, err := fixture.sessions.Create(ctx, "first", "client", settings)
			if !errors.Is(err, changed) {
				t.Fatalf("create error = %v, want scope change", err)
			}
			if fixture.peer.callCount("turn/start") != 0 || fixture.peer.callCount("thread/resume") != 0 {
				t.Fatal("scope change reached a turn or resume", fixture.peer.calls())
			}
			wantStarts, wantUpdates := 1, 0
			if stage == "before-open" {
				wantStarts = 0
				if opens, follows := fixture.factory.counts(); opens != 0 || follows != 0 {
					t.Fatal("stale scope allocated a connection", opens, follows)
				}
			}
			if stage == "after-settings" {
				wantUpdates = 1
			}
			if fixture.peer.callCount("thread/start") != wantStarts || fixture.peer.callCount("thread/settings/update") != wantUpdates {
				t.Fatal("unexpected native mutations after scope change", fixture.peer.calls())
			}
		})
	}
}

func TestTaskAccessCreateRechecksScopeAfterWaitingForFactory(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	fixture.factory.holdOpen()
	defer fixture.factory.releaseOpen()
	changed := errors.New("Fixture native scope changed while opening")
	var stale atomic.Bool
	ctx := protocol.WithScopeValidation(context.Background(), func(context.Context) error {
		if stale.Load() {
			return changed
		}
		return nil
	})
	result := make(chan error, 1)
	go func() {
		_, _, err := fixture.sessions.Create(ctx, "first", "client", protocol.SessionSettings{})
		result <- err
	}()
	select {
	case <-fixture.factory.opening():
	case <-time.After(5 * time.Second):
		t.Fatal("create did not reach the held factory")
	}
	stale.Store(true)
	fixture.factory.releaseOpen()
	select {
	case err := <-result:
		if !errors.Is(err, changed) {
			t.Fatal("late create did not preserve scope refusal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("create did not finish after factory release")
	}
	if fixture.peer.totalCalls() != 0 {
		t.Fatal("scope changed during allocation but native requests escaped", fixture.peer.calls())
	}
}

func TestTaskAccessNativeDispatchRechecksScopeForEveryRequest(t *testing.T) {
	fixture, access := taskAccessFixture(t)
	fixture.sessions.mu.Lock()
	entry := fixture.sessions.entries[taskSessionsTaskID]
	fixture.sessions.mu.Unlock()
	changed := errors.New("Fixture native scope changed before dispatch")
	var stale atomic.Bool
	ctx := protocol.WithScopeValidation(protocol.WithAccessBinding(context.Background(), access.Binding), func(context.Context) error {
		if stale.Load() {
			return changed
		}
		return nil
	})
	release, err := fixture.sessions.bindTaskMutation(ctx, taskSessionsTaskID, entry)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	stale.Store(true)
	fixture.peer.clearCalls()
	for _, method := range []string{"thread/read", "model/list", "thread/start", "thread/resume", "turn/start", "turn/steer", "turn/interrupt", "thread/settings/update"} {
		if _, err := entry.native.Request(method, map[string]any{"threadId": taskSessionsTaskID}); err == nil {
			t.Fatal("stale scope accepted a native request", method)
		}
	}
	if fixture.peer.totalCalls() != 0 {
		t.Fatal("stale scope reached the native peer", fixture.peer.calls())
	}
}

func TestTaskAccessCreateReleasesRequestScopeAfterSuccess(t *testing.T) {
	fixture := newTaskSessionsFixture(t)
	changed := errors.New("Fixture completed request scope expired")
	var stale atomic.Bool
	ctx := protocol.WithScopeValidation(context.Background(), func(context.Context) error {
		if stale.Load() {
			return changed
		}
		return nil
	})
	if _, _, err := fixture.sessions.Create(ctx, "first", "client", protocol.SessionSettings{}); err != nil {
		t.Fatal(err)
	}
	stale.Store(true)
	if _, err := fixture.sessions.Send(context.Background(), taskSessionsTaskID, "legacy", "second"); err != nil {
		t.Fatal("completed create leaked its request context into a later mutation", err)
	}
	if fixture.peer.callCount("turn/steer") != 1 {
		t.Fatal("legacy mutation was not dispatched once", fixture.peer.calls())
	}
}

func TestTaskAccessFinalDispatchRequiresExactNativeTurn(t *testing.T) {
	fixture, access := taskAccessFixture(t)
	fixture.sessions.mu.Lock()
	entry := fixture.sessions.entries[taskSessionsTaskID]
	fixture.sessions.mu.Unlock()
	release, err := fixture.sessions.bindTaskMutation(protocol.WithAccessBinding(context.Background(), access.Binding), taskSessionsTaskID, entry)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, method := range []string{"turn/steer", "turn/interrupt"} {
		if _, err := entry.native.Request(method, map[string]any{"threadId": taskSessionsTaskID,
			"turnId": "different", "expectedTurnId": "different"}); err == nil {
			t.Fatal("final dispatch accepted another turn", method)
		}
		if fixture.peer.callCount(method) != 0 {
			t.Fatal("another turn reached native mutation", fixture.peer.calls())
		}
	}
}
