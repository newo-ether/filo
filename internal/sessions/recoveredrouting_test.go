package sessions

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/protocol"
)

// recoveredRoutingFixture exposes recovery ownership only after an explicit
// mutation succeeds. Creation provenance never belongs to this desktop task.
type recoveredRoutingFixture struct {
	createdFixture
	owned      atomic.Bool
	admissions atomic.Int32
	failure    error
	lookupErr  error
	readErr    error
	runtime    *protocol.Runtime
	onRead     func()
}

func (c *recoveredRoutingFixture) Created(context.Context, string) (bool, error) {
	return false, c.lookupErr
}

func (c *recoveredRoutingFixture) Owns(_ context.Context, id string) (bool, error) {
	return id == "thread" && c.owned.Load(), c.lookupErr
}

func (c *recoveredRoutingFixture) UpdateSettings(ctx context.Context, id string, _ protocol.SessionSettings) error {
	if IdleRecoveryAllowed(ctx, "another-thread") || (!c.owned.Load() && !IdleRecoveryAllowed(ctx, id)) {
		return errors.New("missing exact-task recovery admission")
	}
	c.admissions.Add(1)
	if c.failure != nil {
		return c.failure
	}
	c.owned.Store(true)
	return nil
}

func (c *recoveredRoutingFixture) Send(ctx context.Context, id, _ string, client string) (protocol.SendReceipt, error) {
	if err := c.UpdateSettings(ctx, id, protocol.SessionSettings{}); err != nil {
		return protocol.SendReceipt{}, err
	}
	return protocol.SendReceipt{TurnID: "recovered-turn", ClientID: client}, nil
}

func (c *recoveredRoutingFixture) Read(ctx context.Context, id, cursor string, activity bool) (protocol.ConversationPage, error) {
	page, err := c.createdFixture.Read(ctx, id, cursor, activity)
	if c.onRead != nil {
		c.onRead()
	}
	if c.readErr != nil {
		return page, c.readErr
	}
	turn := "recovered-turn"
	page.Runtime = &protocol.Runtime{Status: "active", ActiveTurnID: &turn}
	if c.runtime != nil {
		page.Runtime = c.runtime
	}
	return page, err
}

func (c *recoveredRoutingFixture) Statuses(ctx context.Context, ids []string) ([]protocol.SessionStatus, error) {
	c.record("statuses " + ids[0])
	status, turn := "active", "recovered-turn"
	return []protocol.SessionStatus{{ID: ids[0], Status: &status, ActiveTurnID: &turn}}, nil
}

func discoverThenLoseOwner(t *testing.T, f *controlSessions) {
	t.Helper()
	release, err := f.sessions.Attach(context.Background(), "thread")
	if err != nil {
		t.Fatal(err)
	}
	release()
	f.ipc.setOwner("")
	f.history.read = func(context.Context, string, string, bool, []string) (protocol.ConversationPage, error) {
		return protocol.ConversationPage{Runtime: &protocol.Runtime{Status: "idle"}}, nil
	}
	f.history.list = func(context.Context, string) (protocol.SessionPage, error) {
		return protocol.SessionPage{Sessions: []protocol.Session{{ID: "thread"}}}, nil
	}
	f.sessions.f.mu.Lock()
	seen := f.sessions.f.seen["thread"]
	f.sessions.f.mu.Unlock()
	if !seen {
		t.Fatal("production discovery did not retain desktop ownership evidence")
	}
}

func TestRecoveredRoutingAfterDesktopOwnerDisappears(t *testing.T) {
	for _, mutation := range []string{"send", "settings"} {
		t.Run(mutation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				created := &recoveredRoutingFixture{}
				f := newControlSessions(desktopState("thread", "completed", "idle"), nil, created)
				defer f.sessions.Close()
				discoverThenLoseOwner(t, f)
				if mutation == "send" {
					receipt, err := f.sessions.Send(context.Background(), "thread", "next", "client")
					if err != nil || receipt.TurnID != "recovered-turn" || receipt.ClientID != "client" {
						t.Fatal(receipt, err)
					}
				} else if err := f.sessions.UpdateSettings(context.Background(), "thread", protocol.SessionSettings{}); err != nil {
					t.Fatal(err)
				}
				page, err := f.sessions.Read(context.Background(), "thread", "", true, nil)
				if err != nil || page.Runtime == nil || page.Runtime.Status != "active" || page.Runtime.ActiveTurnID == nil || *page.Runtime.ActiveTurnID != "recovered-turn" {
					t.Fatal("recovered read was masked by stale desktop discovery", page, err)
				}
				release, err := f.sessions.Attach(context.Background(), "thread")
				if err != nil {
					t.Fatal(err)
				}
				release()
				if created.released.Load() != 1 {
					t.Fatal("recovered attachment lost its release action")
				}
				if err := f.sessions.Stop(context.Background(), "thread", "recovered-turn"); err != nil {
					t.Fatal("recovered stop refused", err)
				}
				states, err := f.sessions.Statuses(context.Background(), []string{"thread"})
				if err != nil || len(states) != 1 || states[0].Status == nil || *states[0].Status != "active" || states[0].ActiveTurnID == nil || *states[0].ActiveTurnID != "recovered-turn" {
					t.Fatal("recovered visible status lost", states, err)
				}
				listed, err := f.sessions.List(context.Background(), "")
				if err != nil || len(listed.Statuses) != 1 || listed.Statuses[0].ActiveTurnID == nil || *listed.Statuses[0].ActiveTurnID != "recovered-turn" {
					t.Fatal("recovered listed status lost", listed, err)
				}
				want := []string{"read thread  true", "attach thread", "stop thread recovered-turn", "statuses thread", "statuses thread"}
				if got := created.entries(); !reflect.DeepEqual(got, want) || created.admissions.Load() != 1 {
					t.Fatal("wrong recovered routing or browsing allocated a writer", got, created.admissions.Load())
				}
			})
		})
	}
}

func TestRecoveredRoutingLiveDesktopOwnerTakesPriority(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		created := &recoveredRoutingFixture{}
		f := newControlSessions(desktopState("thread", "completed", "idle"), nil, created)
		defer f.sessions.Close()
		discoverThenLoseOwner(t, f)
		if _, err := f.sessions.Send(context.Background(), "thread", "next", "client"); err != nil {
			t.Fatal(err)
		}
		f.ipc.setOwner("returned-desktop")
		f.ipc.stateMu.Lock()
		f.ipc.state = desktopState("thread", "inProgress", "active")
		f.ipc.stateMu.Unlock()
		page, err := f.sessions.Read(context.Background(), "thread", "", false, nil)
		if err != nil || page.Runtime == nil || page.Runtime.ActiveTurnID == nil || *page.Runtime.ActiveTurnID != "turn" {
			t.Fatal("live desktop read lost priority", page, err)
		}
		release, err := f.sessions.Attach(context.Background(), "thread")
		if err != nil {
			t.Fatal(err)
		}
		release()
		if err := f.sessions.Stop(context.Background(), "thread", "turn"); err != nil {
			t.Fatal(err)
		}
		receipt, err := f.sessions.Send(context.Background(), "thread", "steer", "second")
		if err != nil || receipt.TurnID != "turn" {
			t.Fatal(receipt, err)
		}
		states, err := f.sessions.Statuses(context.Background(), []string{"thread"})
		if err != nil || len(states) != 1 || states[0].ActiveTurnID == nil || *states[0].ActiveTurnID != "turn" {
			t.Fatal("live desktop status lost priority", states, err)
		}
		listed, err := f.sessions.List(context.Background(), "")
		if err != nil || len(listed.Statuses) != 1 || listed.Statuses[0].ActiveTurnID == nil || *listed.Statuses[0].ActiveTurnID != "turn" {
			t.Fatal("live desktop listed status lost priority", listed, err)
		}
		operations := f.ipc.operations()
		if len(operations) != 2 || operations[0].method != "thread-follower-interrupt-turn" || operations[1].method != "thread-follower-steer-turn" {
			t.Fatal("live desktop controls were rerouted", operations)
		}
		if len(created.entries()) != 0 || created.admissions.Load() != 1 || created.released.Load() != 0 {
			t.Fatal("live desktop operations escaped to recovered executor", created.entries(), created.admissions.Load())
		}
	})
}

func TestRecoveredRoutingFailureAndBrowsingGrantNoAuthority(t *testing.T) {
	for _, outcome := range []string{"failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				created := &recoveredRoutingFixture{failure: errors.New(outcome + " native recovery")}
				f := newControlSessions(desktopState("thread", "completed", "idle"), nil, created)
				defer f.sessions.Close()
				discoverThenLoseOwner(t, f)
				if _, err := f.sessions.Send(context.Background(), "thread", "next", "client"); !errors.Is(err, created.failure) {
					t.Fatal("recovery failure was hidden", err)
				}
				for attempt := 0; attempt < 2; attempt++ {
					page, err := f.sessions.Read(context.Background(), "thread", "", false, nil)
					if err != nil || page.Runtime == nil || page.Runtime.Status != "notLoaded" {
						t.Fatal("unconfirmed recovery acquired read authority", page, err)
					}
					release, err := f.sessions.Attach(context.Background(), "thread")
					if err != nil {
						t.Fatal(err)
					}
					release()
					if err := f.sessions.Stop(context.Background(), "thread", "recovered-turn"); err == nil {
						t.Fatal("unconfirmed recovery acquired stop authority")
					}
					states, err := f.sessions.Statuses(context.Background(), []string{"thread"})
					if err != nil || len(states) != 1 || states[0].ActiveTurnID != nil {
						t.Fatal("unconfirmed recovery acquired status authority", states, err)
					}
					if _, err := f.sessions.List(context.Background(), ""); err != nil {
						t.Fatal(err)
					}
				}
				if created.admissions.Load() != 1 || created.owned.Load() || len(created.entries()) != 0 {
					t.Fatal("browsing retried recovery or failure granted authority", created.admissions.Load(), created.entries())
				}
			})
		})
	}
}

func TestRecoveredRoutingActiveSendUsesExistingWriterWithoutIdleAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		created := &recoveredRoutingFixture{}
		f := newControlSessions(desktopState("thread", "completed", "idle"), nil, created)
		defer f.sessions.Close()
		discoverThenLoseOwner(t, f)
		if _, err := f.sessions.Send(context.Background(), "thread", "next", "first"); err != nil {
			t.Fatal(err)
		}
		// The persisted start cannot prove idle, even if its old completion was indexed.
		f.history.read = func(context.Context, string, string, bool, []string) (protocol.ConversationPage, error) {
			t.Error("active recovered send consulted idle history")
			return protocol.ConversationPage{Runtime: &protocol.Runtime{Status: "notLoaded", CompletedTurnID: protocol.Known[*string](nil)}}, nil
		}
		ctx, err := recoveredMutationContext(f, "thread")
		if err != nil || IdleRecoveryAllowed(ctx, "thread") {
			t.Fatal("active writer gained replacement admission", err)
		}
		receipt, err := f.sessions.Send(context.Background(), "thread", "steer", "second")
		if err != nil || receipt.TurnID != "recovered-turn" || receipt.ClientID != "second" || created.admissions.Load() != 2 {
			t.Fatal(receipt, err, created.admissions.Load())
		}
	})
}

func recoveredMutationContext(f *controlSessions, id string) (context.Context, error) {
	_, ctx, err := f.sessions.mutationDestination(context.Background(), id)
	return ctx, err
}

func TestRecoveredRoutingActiveLookupAndOwnerRacesFailClosed(t *testing.T) {
	for _, mode := range []string{"unknown", "activeWithoutTurn", "readFailure", "ownerAppeared", "uncertainSend"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				created := &recoveredRoutingFixture{}
				created.owned.Store(true)
				f := newControlSessions(desktopState("thread", "completed", "idle"), nil, created)
				defer f.sessions.Close()
				discoverThenLoseOwner(t, f)
				switch mode {
				case "unknown":
					created.runtime = &protocol.Runtime{Status: "notLoaded"}
				case "activeWithoutTurn":
					created.runtime = &protocol.Runtime{Status: "active"}
				case "readFailure":
					created.readErr = errors.New("unconfirmed executor")
				case "ownerAppeared":
					created.onRead = func() { f.ipc.setOwner("returned-desktop") }
				case "uncertainSend":
					created.failure = errors.New("unknown native steer outcome")
				}
				if _, err := f.sessions.Send(context.Background(), "thread", "steer", "client"); err == nil {
					t.Fatal("ambiguous recovered mutation succeeded")
				}
				want := int32(0)
				if mode == "uncertainSend" {
					want = 1
				}
				if created.admissions.Load() != want {
					t.Fatal("mutation was dispatched or replayed", created.admissions.Load())
				}
			})
		})
	}
}

func TestRecoveredRoutingIdleReallocationRequiresFreshTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		terminal := "completed-turn"
		created := &recoveredRoutingFixture{runtime: &protocol.Runtime{Status: "notLoaded", CompletedTurnID: protocol.Known(&terminal)}}
		created.owned.Store(true)
		f := newControlSessions(desktopState("thread", "completed", "idle"), nil, created)
		defer f.sessions.Close()
		discoverThenLoseOwner(t, f)
		ctx, err := recoveredMutationContext(f, "thread")
		if err != nil || !IdleRecoveryAllowed(ctx, "thread") || IdleRecoveryAllowed(ctx, "another-thread") {
			t.Fatal("terminal recovery lost exact-task admission", err)
		}
	})
}

func TestRecoveredRoutingLookupFailureFailsClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		created := &recoveredRoutingFixture{}
		created.owned.Store(true)
		f := newControlSessions(desktopState("thread", "completed", "idle"), nil, created)
		defer f.sessions.Close()
		discoverThenLoseOwner(t, f)
		created.lookupErr = errors.New("recovery scope unavailable")
		if _, err := f.sessions.Read(context.Background(), "thread", "", false, nil); !errors.Is(err, created.lookupErr) {
			t.Fatal(err)
		}
		if _, err := f.sessions.Attach(context.Background(), "thread"); !errors.Is(err, created.lookupErr) {
			t.Fatal(err)
		}
		if err := f.sessions.Stop(context.Background(), "thread", "recovered-turn"); !errors.Is(err, created.lookupErr) {
			t.Fatal(err)
		}
		states, err := f.sessions.Statuses(context.Background(), []string{"thread"})
		if err != nil || len(states) != 1 || states[0].Status != nil || states[0].ActiveTurnID != nil {
			t.Fatal("failed scope lookup invented status", states, err)
		}
		if len(created.entries()) != 0 || created.admissions.Load() != 0 {
			t.Fatal("failed scope lookup reached executor", created.entries())
		}
	})
}
