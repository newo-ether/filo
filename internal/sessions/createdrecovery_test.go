package sessions

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/protocol"
)

// Created tasks need the same fresh idle evidence as recovered tasks before a
// dead executor can be replaced. Creation provenance alone is not idle proof.
type createdRecoveryFixture struct {
	recoveredRoutingFixture
	idleAdmission bool
}

func (c *createdRecoveryFixture) Created(context.Context, string) (bool, error) {
	return true, nil
}

func (c *createdRecoveryFixture) UpdateSettings(ctx context.Context, id string, _ protocol.SessionSettings) error {
	if IdleRecoveryAllowed(ctx, id) != c.idleAdmission || IdleRecoveryAllowed(ctx, "another-thread") {
		return errors.New("wrong created-task recovery admission")
	}
	c.admissions.Add(1)
	return c.failure
}

func (c *createdRecoveryFixture) Send(ctx context.Context, id, _ string, client string) (protocol.SendReceipt, error) {
	if err := c.UpdateSettings(ctx, id, protocol.SessionSettings{}); err != nil {
		return protocol.SendReceipt{}, err
	}
	return protocol.SendReceipt{TurnID: "next", ClientID: client}, nil
}

func TestCreatedTaskMutationRevalidatesRuntimeBeforeReplacement(t *testing.T) {
	turn := "turn"
	for _, test := range []struct {
		name    string
		runtime *protocol.Runtime
		allowed bool
		idle    bool
	}{
		{"idle", &protocol.Runtime{Status: "idle"}, true, true},
		{"terminal", &protocol.Runtime{Status: "notLoaded", CompletedTurnID: protocol.Known(&turn)}, true, true},
		{"active", &protocol.Runtime{Status: "active", ActiveTurnID: &turn}, true, false},
		{"unknown", &protocol.Runtime{Status: "notLoaded"}, false, false},
		{"active without identity", &protocol.Runtime{Status: "active"}, false, false},
	} {
		for _, mutation := range []string{"send", "settings"} {
			t.Run(test.name+"/"+mutation, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					created := &createdRecoveryFixture{idleAdmission: test.idle}
					created.owned.Store(true)
					created.runtime = test.runtime
					f := newControlSessions(desktopState("thread", "completed", "idle"), nil, created)
					defer f.sessions.Close()
					f.ipc.setOwner("")
					var err error
					if mutation == "send" {
						_, err = f.sessions.Send(context.Background(), "thread", "next", "client")
					} else {
						err = f.sessions.UpdateSettings(context.Background(), "thread", protocol.SessionSettings{})
					}
					if (err == nil) != test.allowed || (created.admissions.Load() == 1) != test.allowed {
						t.Fatalf("allowed=%v, dispatches=%d, err=%v", test.allowed, created.admissions.Load(), err)
					}
					if got := created.entries(); len(got) != 1 || got[0] != "read thread  false" {
						t.Fatal("created task did not revalidate its exact executor", got)
					}
				})
			})
		}
	}
}

func TestCreatedTaskRecoveryRefusesAppearingOwnerAndDoesNotReplayFailure(t *testing.T) {
	for _, mode := range []string{"owner appeared", "native conflict", "read failure"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				created := &createdRecoveryFixture{idleAdmission: true}
				created.owned.Store(true)
				created.runtime = &protocol.Runtime{Status: "idle"}
				f := newControlSessions(desktopState("thread", "completed", "idle"), nil, created)
				defer f.sessions.Close()
				f.ipc.setOwner("")
				want := int32(0)
				switch mode {
				case "owner appeared":
					created.onRead = func() { f.ipc.setOwner("returned-owner") }
				case "native conflict":
					created.failure = errors.New("native writer lock conflict")
					want = 1
				case "read failure":
					created.readErr = errors.New("unconfirmed native state")
				}
				if _, err := f.sessions.Send(context.Background(), "thread", "next", "client"); err == nil || created.admissions.Load() != want {
					t.Fatal("unconfirmed recovery was dispatched or replayed", err, created.admissions.Load())
				}
			})
		})
	}
}
