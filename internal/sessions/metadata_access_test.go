package sessions

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestCatalogManagementBindingCannotStartOrResumeExecution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, _ := newAccessSessions(desktopState("thread", "completed", "idle"), nil)
		defer f.sessions.Close()
		access, err := f.sessions.CatalogAccess(context.Background(), "thread")
		if err != nil {
			t.Fatal(err)
		}
		ctx := protocol.WithAccessBinding(context.Background(), access.Binding)
		if err := f.sessions.ValidateAccess(ctx, "thread", access.Binding); err != nil {
			t.Fatal("catalog binding rejected", err)
		}
		if len(f.ipc.requests) != 0 {
			t.Fatal("catalog validation discovered an execution owner")
		}
		if _, err := f.sessions.Send(ctx, "thread", "must not send", "client"); err == nil {
			t.Fatal("catalog binding admitted a native turn")
		}
		if err := f.sessions.Stop(ctx, "thread", "turn"); err == nil {
			t.Fatal("catalog binding stopped execution")
		}
		if err := f.sessions.UpdateSettings(ctx, "thread", protocol.SessionSettings{Model: protocol.Known("model")}); err == nil {
			t.Fatal("catalog binding changed execution settings")
		}
		if len(f.ipc.operations()) != 0 {
			t.Fatal("catalog binding dispatched a native operation")
		}
		if err := f.sessions.ValidateAccess(ctx, "other", access.Binding); err == nil {
			t.Fatal("catalog binding crossed thread identity")
		}
		f.sessions.options.AccessScope = func(context.Context) (string, error) { return "changed_store", nil }
		if _, err := f.sessions.Rename(ctx, "thread", "stale"); err == nil {
			t.Fatal("old catalog binding mutated another account")
		}
	})
}

type metadataConnectIPC struct {
	*accessIPC
	onConnect func()
}

func (ipc *metadataConnectIPC) Connect(ctx context.Context) error {
	ipc.onConnect()
	return ipc.accessIPC.Connect(ctx)
}

func TestCatalogArchiveRevalidatesScopeAfterConnecting(t *testing.T) {
	for _, mode := range []string{"changed-account", "cancelled", "scope-revoked"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, ipc := newAccessSessions(desktopState("thread", "completed", "idle"), nil)
				defer f.sessions.Close()
				access, err := f.sessions.CatalogAccess(context.Background(), "thread")
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(protocol.WithAccessBinding(context.Background(), access.Binding))
				defer cancel()
				f.sessions.options.IPC = &metadataConnectIPC{accessIPC: ipc, onConnect: func() {
					switch mode {
					case "changed-account":
						f.sessions.options.AccessScope = func(context.Context) (string, error) { return "another_store", nil }
					case "cancelled":
						cancel()
					}
				}}
				if mode == "scope-revoked" {
					ctx = protocol.WithScopeValidation(ctx, func(context.Context) error { return errors.New("scope revoked") })
				}
				if _, err := f.sessions.Archive(ctx, "thread"); err == nil {
					t.Fatal("stale catalog grant reached archive")
				}
				if len(f.sessions.options.Base.(*baseFixture).archives()) != 0 {
					t.Fatal("native archive dispatched after admission changed")
				}
			})
		})
	}
}
