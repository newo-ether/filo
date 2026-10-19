package sessions

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/protocol"
)

type recoveryCreatedFixture struct {
	createdFixture
	admissions atomic.Int32
	failure    error
}

func (c *recoveryCreatedFixture) Created(context.Context, string) (bool, error) { return false, nil }
func (c *recoveryCreatedFixture) UpdateSettings(ctx context.Context, id string, settings protocol.SessionSettings) error {
	if !IdleRecoveryAllowed(ctx, id) || IdleRecoveryAllowed(ctx, "another-thread") {
		return errors.New("missing exact-task mutation admission")
	}
	c.admissions.Add(1)
	return c.failure
}
func (c *recoveryCreatedFixture) Send(ctx context.Context, id, value, client string) (protocol.SendReceipt, error) {
	if err := c.UpdateSettings(ctx, id, protocol.SessionSettings{}); err != nil {
		return protocol.SendReceipt{}, err
	}
	return protocol.SendReceipt{TurnID: "next", ClientID: client}, nil
}

func TestRecoveryRequiresPositiveIdleOrLatestTerminalEvidence(t *testing.T) {
	turn := "terminal"
	for _, test := range []struct {
		name    string
		runtime *protocol.Runtime
		allowed bool
	}{
		{"missing", nil, false},
		{"unknown", &protocol.Runtime{}, false},
		{"absent-owner", &protocol.Runtime{Status: "notLoaded"}, false},
		{"newer-start", &protocol.Runtime{Status: "notLoaded", CompletedTurnID: protocol.Known[*string](nil)}, false},
		{"active", &protocol.Runtime{Status: "active", CompletedTurnID: protocol.Known(&turn)}, false},
		{"active-id", &protocol.Runtime{Status: "idle", ActiveTurnID: &turn}, false},
		{"terminal", &protocol.Runtime{Status: "notLoaded", CompletedTurnID: protocol.Known(&turn)}, true},
		{"idle", &protocol.Runtime{Status: "idle"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				created := &recoveryCreatedFixture{}
				f := newControlSessions(desktopState("thread", "completed", "idle"), nil, created)
				defer f.sessions.Close()
				f.ipc.setOwner("")
				f.history.read = func(context.Context, string, string, bool, []string) (protocol.ConversationPage, error) {
					return protocol.ConversationPage{Runtime: test.runtime}, nil
				}
				err := f.sessions.UpdateSettings(context.Background(), "thread", protocol.SessionSettings{})
				if (err == nil) != test.allowed || (created.admissions.Load() == 1) != test.allowed {
					t.Fatalf("allowed=%v, dispatches=%d, err=%v", test.allowed, created.admissions.Load(), err)
				}
			})
		})
	}
}

func TestRecoveryRetainsNativeOwnerPriorityAndNeverReplaysFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conflict := errors.New("native writer lock conflict")
		created := &recoveryCreatedFixture{failure: conflict}
		f := newControlSessions(desktopState("thread", "completed", "idle"), nil, created)
		defer f.sessions.Close()
		if _, err := f.sessions.Send(context.Background(), "thread", "hello", "client"); err != nil || created.admissions.Load() != 0 {
			t.Fatal("live owner did not take priority", err)
		}
		f.ipc.setOwner("")
		turn := "terminal"
		f.history.read = func(context.Context, string, string, bool, []string) (protocol.ConversationPage, error) {
			return protocol.ConversationPage{Runtime: &protocol.Runtime{Status: "notLoaded", CompletedTurnID: protocol.Known(&turn)}}, nil
		}
		if _, err := f.sessions.Send(context.Background(), "thread", "hello", "client"); !errors.Is(err, conflict) || created.admissions.Load() != 1 {
			t.Fatal("native conflict was replayed or hidden", err, created.admissions.Load())
		}
		f.history.read = func(context.Context, string, string, bool, []string) (protocol.ConversationPage, error) {
			f.ipc.setOwner("new-owner")
			return protocol.ConversationPage{Runtime: &protocol.Runtime{Status: "idle"}}, nil
		}
		if err := f.sessions.UpdateSettings(context.Background(), "thread", protocol.SessionSettings{}); err == nil || created.admissions.Load() != 1 {
			t.Fatal("appearing owner did not fence recovery", err)
		}
	})
}

func TestRecoveryBrowsingAndStopNeverAdmitWriter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		created := &recoveryCreatedFixture{}
		f := newControlSessions(desktopState("thread", "completed", "idle"), nil, created)
		defer f.sessions.Close()
		f.ipc.setOwner("")
		f.history.read = func(context.Context, string, string, bool, []string) (protocol.ConversationPage, error) {
			return protocol.ConversationPage{Runtime: &protocol.Runtime{Status: "idle"}}, nil
		}
		if _, err := f.sessions.Read(context.Background(), "thread", "", false, nil); err != nil {
			t.Fatal(err)
		}
		release, err := f.sessions.Attach(context.Background(), "thread")
		if err != nil {
			t.Fatal(err)
		}
		release()
		if err := f.sessions.Stop(context.Background(), "thread", "old-turn"); err == nil {
			t.Fatal("stop admitted missing owner")
		}
		if created.admissions.Load() != 0 || len(created.entries()) != 0 {
			t.Fatal("read/stop acquired execution")
		}
	})
}
