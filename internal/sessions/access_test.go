package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/desktop"
	"github.com/newo-ether/filo/internal/protocol"
)

type accessIPC struct {
	*controlIPC
	generation     atomic.Uint64
	boundCalls     atomic.Int32
	beforeBound    func()
	afterDiscovery func()
}

func (ipc *accessIPC) ConnectionGeneration() uint64 { return ipc.generation.Load() }
func (ipc *accessIPC) Request(ctx context.Context, method string, version int, params any, target string) (desktop.Message, error) {
	result, err := ipc.controlIPC.Request(ctx, method, version, params, target)
	if method == "thread-owner-discovery" && ipc.afterDiscovery != nil {
		ipc.afterDiscovery()
	}
	return result, err
}
func (ipc *accessIPC) RequestOnConnection(ctx context.Context, generation uint64, method string, version int, params any, target string) (desktop.Message, error) {
	if ipc.beforeBound != nil {
		ipc.beforeBound()
	}
	if generation == 0 || generation != ipc.generation.Load() {
		return nil, errors.New("connection generation changed")
	}
	ipc.boundCalls.Add(1)
	return ipc.controlIPC.Request(ctx, method, version, params, target)
}

func newAccessSessions(state map[string]any, created CreatedReader) (*controlSessions, *accessIPC) {
	base := &baseFixture{models: []protocol.Model{{ID: "model", ReasoningEfforts: []string{"low"}}}}
	f := newControlSessions(state, base, created)
	ipc := &accessIPC{controlIPC: f.ipc}
	ipc.generation.Store(1)
	f.sessions.options.IPC = ipc
	f.sessions.f.ipc = ipc
	f.sessions.options.AccessScope = func(context.Context) (string, error) { return "verified_store", nil }
	return f, ipc
}

func accessContext(t *testing.T, sessions *DesktopSessions, id string) context.Context {
	t.Helper()
	access, err := sessions.Access(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.WithAccessBinding(context.Background(), access.Binding)
}

func hasControl(caps protocol.AdapterCapabilities) bool {
	return caps.IdleSend || caps.ActiveSend != "unsupported" || caps.Stop || len(caps.Settings) > 0 ||
		caps.Rename || caps.Archive || caps.Uploads
}

func TestCatalogAccessIsCanonicalAndNeverLoadsNativeOwners(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		created := &createdFixture{own: true}
		f, _ := newAccessSessions(desktopState("thread", "completed", "idle"), created)
		defer f.sessions.Close()
		for _, id := range []string{"thread", "other-thread"} {
			access, err := f.sessions.CatalogAccess(context.Background(), id)
			if err != nil || access.NativeScopeID != "verified_store" || hasControl(access.Capabilities) {
				t.Fatal(access, err)
			}
			encoded, _ := json.Marshal([3]string{"codex", "verified_store", id})
			hash := sha256.Sum256(encoded)
			if access.CanonicalSessionRef != base64.RawURLEncoding.EncodeToString(hash[:]) ||
				strings.Contains(access.Binding, "original") {
				t.Fatal("canonical identity differs from the CLI domain", access)
			}
		}
		if len(f.ipc.requests) != 0 || len(f.ipc.broadcasts()) != 0 || f.history.reads.Load() != 0 || created.calls.Load() != 0 {
			t.Fatal("catalog identity performed native session I/O")
		}
	})
}

func TestAccessRequiresVerifiedScopeBeforeDiscoveringAnyOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, scope := range []func(context.Context) (string, error){nil,
			func(context.Context) (string, error) { return "", nil },
			func(context.Context) (string, error) { return "C:/private/account", nil },
			func(context.Context) (string, error) { return "old_scope", errors.New("account changed") },
		} {
			f, _ := newAccessSessions(desktopState("thread", "completed", "idle"), nil)
			f.sessions.options.AccessScope = scope
			if _, err := f.sessions.Access(context.Background(), "thread"); err == nil {
				t.Fatal("unverified native store granted access")
			}
			if len(f.ipc.requests) != 0 {
				t.Fatal("missing store proof contacted a native owner")
			}
			f.sessions.Close()
		}
	})
}

func TestAccessCapabilitiesFollowNativeRuntimeAndDispatchSupport(t *testing.T) {
	for _, mode := range []string{"idle", "active", "unknown", "active-no-turn", "no-bound-transport"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				state := desktopState("thread", "completed", "idle")
				if mode == "active" {
					state = desktopState("thread", "inProgress", "active")
				} else if mode == "unknown" {
					state["threadRuntimeStatus"] = map[string]any{"type": "notLoaded"}
				} else if mode == "active-no-turn" {
					state["threadRuntimeStatus"] = map[string]any{"type": "active"}
				}
				f, _ := newAccessSessions(state, nil)
				defer f.sessions.Close()
				if mode == "no-bound-transport" {
					f.sessions.options.IPC = f.ipc
					f.sessions.f.ipc = f.ipc
				}
				access, err := f.sessions.Access(context.Background(), "thread")
				if err != nil {
					t.Fatal(err)
				}
				caps := access.Capabilities
				if !caps.History || !caps.Live || !caps.Models || caps.Create || caps.Rename || caps.Archive {
					t.Fatal(caps)
				}
				if caps.IdleSend != (mode == "idle") || caps.Stop != (mode == "active") ||
					(caps.ActiveSend == "native-steer") != (mode == "active") ||
					(len(caps.Settings) == 3) != (mode == "idle" || mode == "active") {
					t.Fatal("capabilities were not derived from the exact runtime", caps)
				}
				if mode == "no-bound-transport" {
					ctx := protocol.WithAccessBinding(context.Background(), access.Binding)
					if _, err := f.sessions.Send(ctx, "thread", "hello", "client"); err == nil {
						t.Fatal("unfenced scoped transport sent input")
					}
					if _, err := f.sessions.Send(context.Background(), "thread", "legacy", "legacy"); err != nil {
						t.Fatal("legacy owner path regressed", err)
					}
				}
			})
		})
	}
}

func TestAccessBindingTracksTurnEpochAndFollowerGeneration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, ipc := newAccessSessions(desktopState("thread", "inProgress", "active"), nil)
		defer f.sessions.Close()
		first, err := f.sessions.Access(context.Background(), "thread")
		if err != nil {
			t.Fatal(err)
		}
		state := desktopState("thread", "inProgress", "active")
		state["hasUnreadTurn"] = true
		f.sessions.Receive(snapshotOf("thread", "original", 2, state))
		if err := f.sessions.ValidateAccess(context.Background(), "thread", first.Binding); err != nil {
			t.Fatal("ordinary streaming metadata revoked the binding", err)
		}
		f.sessions.Receive(snapshotOf("thread", "original", 3, desktopState("thread", "completed", "idle")))
		f.sessions.Receive(snapshotOf("thread", "original", 4, desktopState("thread", "inProgress", "active")))
		if err := f.sessions.ValidateAccess(context.Background(), "thread", first.Binding); err == nil {
			t.Fatal("turn ABA revived the original binding")
		}
		current, _ := f.sessions.Access(context.Background(), "thread")
		f.sessions.Disconnected()
		ipc.generation.Add(1)
		if err := f.sessions.ValidateAccess(context.Background(), "thread", current.Binding); err == nil {
			t.Fatal("same owner on a replacement connection retained authority")
		}
		replacement, _ := f.sessions.Access(context.Background(), "thread")
		if replacement.CanonicalSessionRef != first.CanonicalSessionRef || replacement.Binding == current.Binding {
			t.Fatal("canonical identity and access generation were conflated")
		}
	})
}

func TestScopedControlsDispatchExactlyOnceOnTheAdmittedConnection(t *testing.T) {
	for _, operation := range []string{"idle-send", "active-send", "settings", "stop"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				state := desktopState("thread", "completed", "idle")
				if operation == "active-send" || operation == "stop" {
					state = desktopState("thread", "inProgress", "active")
				}
				state["latestModel"] = "model"
				state["latestReasoningEffort"] = "low"
				f, ipc := newAccessSessions(state, nil)
				defer f.sessions.Close()
				ctx := accessContext(t, f.sessions, "thread")
				f.ipc.settingsApplied = func() { f.sessions.Receive(snapshotOf("thread", "original", 2, state)) }
				var err error
				switch operation {
				case "idle-send", "active-send":
					_, err = f.sessions.Send(ctx, "thread", "hello", "client")
				case "settings":
					err = f.sessions.UpdateSettings(ctx, "thread", protocol.SessionSettings{Effort: protocol.Known("low")})
				case "stop":
					err = f.sessions.Stop(ctx, "thread", "turn")
				}
				if err != nil || ipc.boundCalls.Load() != 1 || len(f.ipc.operations()) != 1 {
					t.Fatal("mutation did not use its exact connection once", err, ipc.boundCalls.Load(), f.ipc.operations())
				}
			})
		})
	}
}

func TestScopedStaleBindingsNeverSwitchOwnerStoreOrExecutor(t *testing.T) {
	for _, change := range []string{"owner", "connection", "turn", "store", "lost-owner"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				created := &recoveryCreatedFixture{}
				f, ipc := newAccessSessions(desktopState("thread", "inProgress", "active"), created)
				defer f.sessions.Close()
				ctx := accessContext(t, f.sessions, "thread")
				switch change {
				case "owner":
					f.ipc.setOwner("replacement")
				case "connection":
					ipc.generation.Add(1)
				case "turn":
					f.sessions.Receive(snapshotOf("thread", "original", 2, desktopState("thread", "completed", "idle")))
				case "store":
					f.sessions.options.AccessScope = func(context.Context) (string, error) { return "replacement_store", nil }
				case "lost-owner":
					f.ipc.setOwner("")
				}
				if _, err := f.sessions.Send(ctx, "thread", "hello", "client"); err == nil {
					t.Fatal("stale send was accepted")
				}
				if err := f.sessions.UpdateSettings(ctx, "thread", protocol.SessionSettings{Effort: protocol.Known("low")}); err == nil {
					t.Fatal("stale settings were accepted")
				}
				if err := f.sessions.Stop(ctx, "thread", "turn"); err == nil {
					t.Fatal("stale stop was accepted")
				}
				if ipc.boundCalls.Load() != 0 || len(f.ipc.operations()) != 0 || created.admissions.Load() != 0 {
					t.Fatal("stale binding dispatched or recovered a writer")
				}
			})
		})
	}
}

func TestDispatchRevalidatesAfterRouteValidationAndCannotReconnect(t *testing.T) {
	for _, change := range []string{"turn-after-discovery", "scope-at-dispatch", "connection-at-request", "cancel-at-request"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, ipc := newAccessSessions(desktopState("thread", "inProgress", "active"), nil)
				defer f.sessions.Close()
				ctx := accessContext(t, f.sessions, "thread")
				if err := f.sessions.ValidateAccess(ctx, "thread", protocol.AccessBinding(ctx)); err != nil {
					t.Fatal(err)
				}
				admitted, err := f.sessions.admitAccess(ctx, "thread")
				if err != nil {
					t.Fatal(err)
				}
				if change == "turn-after-discovery" {
					ipc.afterDiscovery = func() {
						f.sessions.Receive(snapshotOf("thread", "original", 2, desktopState("thread", "completed", "idle")))
					}
				} else if change == "scope-at-dispatch" {
					f.sessions.options.AccessScope = func(context.Context) (string, error) { return "replacement", nil }
				} else if change == "connection-at-request" {
					ipc.beforeBound = func() { ipc.generation.Add(1) }
				} else {
					var cancel context.CancelFunc
					admitted, cancel = context.WithCancel(admitted)
					cancel()
				}
				_, err = f.sessions.operation(admitted, "thread", "original", "thread-follower-interrupt-turn", 4,
					map[string]any{"expectedTurnId": "turn"})
				if err == nil || ipc.boundCalls.Load() != 0 || len(f.ipc.operations()) != 0 {
					t.Fatal("a post-admission replacement reached native mutation", err, f.ipc.operations())
				}
			})
		})
	}
}

func TestScopedReadOnlyHistoryCannotAdmitIdleRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		created := &recoveryCreatedFixture{}
		f, _ := newAccessSessions(desktopState("thread", "completed", "idle"), created)
		defer f.sessions.Close()
		f.ipc.setOwner("")
		f.history.read = func(context.Context, string, string, bool, []string) (protocol.ConversationPage, error) {
			return protocol.ConversationPage{Runtime: &protocol.Runtime{Status: "idle"}}, nil
		}
		// A catalog/history-only binding cannot be promoted to recovery authority.
		access, err := f.sessions.CatalogAccess(context.Background(), "thread")
		if err != nil {
			t.Fatal(err)
		}
		ctx := protocol.WithAccessBinding(context.Background(), access.Binding)
		if _, err := f.sessions.Send(ctx, "thread", "hello", "client"); err == nil {
			t.Fatal("history-only binding allocated a recovered writer")
		}
		if created.admissions.Load() != 0 {
			t.Fatal("access discovery allocated a writer")
		}
		if _, err := f.sessions.Send(context.Background(), "thread", "legacy", "legacy"); err != nil || created.admissions.Load() != 1 {
			t.Fatal("legacy explicit idle recovery regressed", err)
		}
	})
}

func TestAccessBindingRevokesAfterObservedOwnerLossAndReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, _ := newAccessSessions(desktopState("thread", "inProgress", "active"), nil)
		defer f.sessions.Close()
		first, err := f.sessions.Access(context.Background(), "thread")
		if err != nil {
			t.Fatal(err)
		}
		f.ipc.setOwner("")
		if owner, err := f.sessions.f.owner(context.Background(), "thread"); err != nil || owner != "" {
			t.Fatal(owner, err)
		}
		f.ipc.setOwner("original")
		if err := f.sessions.ValidateAccess(context.Background(), "thread", first.Binding); err == nil {
			t.Fatal("same textual owner revived authority after observed loss")
		}
	})
}

func TestBindingsAreIsolatedBySessionAndSurfaceSecret(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, _ := newAccessSessions(desktopState("thread", "completed", "idle"), nil)
		defer f.sessions.Close()
		other, _ := newAccessSessions(desktopState("thread", "completed", "idle"), nil)
		defer other.sessions.Close()
		ctx := accessContext(t, f.sessions, "thread")
		if _, err := other.sessions.Send(ctx, "thread", "hello", "client"); err == nil {
			t.Fatal("different access surface accepted the original binding")
		}
		if _, err := f.sessions.Send(ctx, "other-thread", "hello", "client"); err == nil {
			t.Fatal("binding crossed native session identity")
		}
		first, _ := f.sessions.Access(context.Background(), "thread")
		second, _ := other.sessions.Access(context.Background(), "thread")
		if !reflect.DeepEqual(first.CanonicalSessionRef, second.CanonicalSessionRef) || first.Binding == second.Binding {
			t.Fatal("canonical sharing leaked authority")
		}
	})
}
