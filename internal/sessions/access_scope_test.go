package sessions

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/protocol"
)

// This fixture is an already admitted native executor. It refuses a translated
// binding at final dispatch after any replacement and never allocates a writer.
type nativeAccessScopeFixture struct {
	createdFixture
	epoch            atomic.Uint64
	beforeDispatch   func()
	beforeValidation func()
}

func (scope *nativeAccessScopeFixture) Access(ctx context.Context, id string) (protocol.SessionAccess, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionAccess{}, err
	}
	return protocol.SessionAccess{Binding: "native_" + strings.Repeat("x", int(scope.epoch.Load()+1)),
		Capabilities: protocol.AdapterCapabilities{History: true, Live: true,
			ActiveSend: "native-steer", Stop: true, Settings: []string{"effort"}}}, nil
}
func (scope *nativeAccessScopeFixture) ValidateAccess(ctx context.Context, id, binding string) error {
	if scope.beforeValidation != nil {
		scope.beforeValidation()
	}
	access, err := scope.Access(ctx, id)
	if err != nil {
		return err
	}
	if binding != access.Binding {
		return errors.New("native executor generation changed")
	}
	return nil
}
func (scope *nativeAccessScopeFixture) dispatch(ctx context.Context, id string) error {
	if scope.beforeDispatch != nil {
		scope.beforeDispatch()
	}
	if IdleRecoveryAllowed(ctx, id) {
		return errors.New("active executor must not acquire idle recovery admission")
	}
	return scope.ValidateAccess(ctx, id, protocol.AccessBinding(ctx))
}
func (scope *nativeAccessScopeFixture) Send(ctx context.Context, id, value, client string) (protocol.SendReceipt, error) {
	if err := scope.dispatch(ctx, id); err != nil {
		return protocol.SendReceipt{}, err
	}
	return scope.createdFixture.Send(ctx, id, value, client)
}
func (scope *nativeAccessScopeFixture) Stop(ctx context.Context, id, turn string) error {
	if err := scope.dispatch(ctx, id); err != nil {
		return err
	}
	return scope.createdFixture.Stop(ctx, id, turn)
}
func (scope *nativeAccessScopeFixture) UpdateSettings(ctx context.Context, id string, settings protocol.SessionSettings) error {
	if err := scope.dispatch(ctx, id); err != nil {
		return err
	}
	return scope.createdFixture.UpdateSettings(ctx, id, settings)
}

func TestScopedCreatedAndRecoveredExecutorUsesItsOwnFinalBinding(t *testing.T) {
	for _, mutation := range []string{"send", "stop", "settings", "unsupported-settings", "replacement"} {
		t.Run(mutation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				scope := &nativeAccessScopeFixture{createdFixture: createdFixture{own: true}}
				f, ipc := newAccessSessions(desktopState("thread", "completed", "idle"), scope)
				defer f.sessions.Close()
				f.ipc.setOwner("")
				access, err := f.sessions.Access(context.Background(), "thread")
				if err != nil || access.Capabilities.ActiveSend != "native-steer" || !access.Capabilities.Stop {
					t.Fatal("explicit native scope did not expose its proven controls", access, err)
				}
				ctx := protocol.WithAccessBinding(context.Background(), access.Binding)
				if mutation == "replacement" {
					scope.beforeDispatch = func() { scope.epoch.Add(1) }
				}
				switch mutation {
				case "send", "replacement":
					_, err = f.sessions.Send(ctx, "thread", "hello", "client")
				case "stop":
					err = f.sessions.Stop(ctx, "thread", "turn")
				case "settings":
					err = f.sessions.UpdateSettings(ctx, "thread", protocol.SessionSettings{Effort: protocol.Known("low")})
				case "unsupported-settings":
					err = f.sessions.UpdateSettings(ctx, "thread", protocol.SessionSettings{Model: protocol.Known("model")})
				}
				denied := mutation == "replacement" || mutation == "unsupported-settings"
				if (err != nil) != denied || (len(scope.entries()) == 0) != denied {
					t.Fatal("native binding or per-field permission was bypassed", err, scope.entries())
				}
				if len(f.ipc.operations()) != 0 || ipc.boundCalls.Load() != 0 || f.history.reads.Load() != 0 {
					t.Fatal("explicit scope switched to Desktop or allocated through history")
				}
			})
		})
	}
}

func TestScopedCreatedExecutorRejectsADesktopOwnerAppearingAtValidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scope := &nativeAccessScopeFixture{createdFixture: createdFixture{own: true}}
		f, _ := newAccessSessions(desktopState("thread", "completed", "idle"), scope)
		defer f.sessions.Close()
		f.ipc.setOwner("")
		ctx := accessContext(t, f.sessions, "thread")
		scope.beforeValidation = func() { f.ipc.setOwner("replacement-desktop") }
		if _, err := f.sessions.Send(ctx, "thread", "hello", "client"); err == nil || len(scope.entries()) != 0 {
			t.Fatal("appearing Desktop owner did not fence the separate executor", err, scope.entries())
		}
	})
}

func TestScopeChangesWhileGatheringAccessEvidenceFailClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, ipc := newAccessSessions(desktopState("thread", "completed", "idle"), nil)
		defer f.sessions.Close()
		scope := "initial_store"
		f.sessions.options.AccessScope = func(context.Context) (string, error) { return scope, nil }
		ipc.afterDiscovery = func() { scope = "replacement_store" }
		if _, err := f.sessions.Access(context.Background(), "thread"); err == nil {
			t.Fatal("mixed-account owner evidence became authoritative")
		}
	})
}
