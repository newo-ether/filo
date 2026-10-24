package sessions

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestScopedIdleRecoveryOffersControlsWithoutAllocatingAndRechecksBeforeDispatch(t *testing.T) {
	for _, operation := range []string{"send", "settings", "stop", "active", "owner", "terminal", "conflict", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				created := &recoveryCreatedFixture{}
				f, _ := newAccessSessions(desktopState("thread", "completed", "idle"), created)
				defer f.sessions.Close()
				f.sessions.options.AccessBinding = protocol.AccessBinding
				f.sessions.options.WithAccessBinding = protocol.WithAccessBinding
				f.ipc.setOwner("")
				turn := "completed"
				runtime := &protocol.Runtime{Status: "idle", CompletedTurnID: protocol.Known(&turn)}
				f.history.read = func(context.Context, string, string, bool, []string) (protocol.ConversationPage, error) {
					return protocol.ConversationPage{Runtime: runtime}, nil
				}
				access, err := f.sessions.Access(context.Background(), "thread")
				if err != nil || !access.Capabilities.IdleSend || !access.Capabilities.Uploads || len(access.Capabilities.Settings) != 3 || access.Capabilities.Stop || access.Capabilities.Live {
					t.Fatal("idle recovery offer is missing or overstates readiness", access, err)
				}
				if created.admissions.Load() != 0 || len(f.ipc.operations()) != 0 {
					t.Fatal("access allocated an executor")
				}
				ctx, cancel := context.WithCancel(protocol.WithAccessBinding(context.Background(), access.Binding))
				defer cancel()
				switch operation {
				case "active":
					runtime = &protocol.Runtime{Status: "active", ActiveTurnID: &turn}
				case "owner":
					f.ipc.setOwner("new-owner")
				case "terminal":
					newTurn := "new-completion"
					runtime = &protocol.Runtime{Status: "idle", CompletedTurnID: protocol.Known(&newTurn)}
				case "conflict":
					created.failure = errors.New("native writer lock conflict")
				case "cancel":
					cancel()
				}
				switch operation {
				case "settings":
					err = f.sessions.UpdateSettings(ctx, "thread", protocol.SessionSettings{Effort: protocol.Known("low")})
				case "stop":
					err = f.sessions.Stop(ctx, "thread", turn)
				default:
					_, err = f.sessions.Send(ctx, "thread", "hello", "client")
				}
				allowed := operation == "send" || operation == "settings"
				if (err == nil) != allowed {
					t.Fatal("mutation ignored exact idle proof", operation, err)
				}
				want := int32(0)
				if allowed || operation == "conflict" {
					want = 1
				}
				if created.admissions.Load() != want || len(f.ipc.operations()) != 0 {
					t.Fatal("mutation replayed or changed native owner", created.admissions.Load(), f.ipc.operations())
				}
			})
		})
	}
}
