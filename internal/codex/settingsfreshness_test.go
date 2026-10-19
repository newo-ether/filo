package codex

import (
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestNativeSettingsRequireFreshEvidenceForTheExactTask(t *testing.T) {
	for _, source := range []string{"empty acknowledgement", "another task notification"} {
		t.Run(source, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				host := newSessionHost()
				host.replies["thread/resume"] = sessionReply(map[string]any{"model": "m"})
				session := NewNativeTaskSession(host)
				for _, id := range []string{"task", "other"} {
					if err := session.Attach(id); err != nil {
						t.Fatal(err)
					}
				}
				// The desired value was confirmed before this request, so matching
				// cached settings alone cannot confirm another mutation.
				session.Receive("thread/settings/updated", map[string]any{
					"threadId": "task", "threadSettings": map[string]any{"model": "m"},
				})
				host.replies["thread/settings/update"] = func(map[string]any) (any, error) {
					if source == "another task notification" {
						session.Receive("thread/settings/updated", map[string]any{
							"threadId": "other", "threadSettings": map[string]any{"model": "m"},
						})
					}
					return map[string]any{}, nil
				}
				if err := session.applySettings("task", map[string]any{"threadId": "task", "model": "m"},
					protocol.SessionSettings{Model: protocol.Known("m")}); err == nil {
					t.Fatal("Old settings confirmed a new request without fresh native evidence")
				}
				if host.count("thread/settings/update") != 1 {
					t.Fatal("Unconfirmed native mutation was replayed")
				}
			})
		})
	}
}

func TestNativeSettingsAcceptFreshNotificationBeforeAcknowledgement(t *testing.T) {
	host := newSessionHost()
	host.replies["thread/resume"] = sessionReply(map[string]any{"model": "m"})
	session := NewNativeTaskSession(host)
	if err := session.Attach("task"); err != nil {
		t.Fatal(err)
	}
	host.replies["thread/settings/update"] = func(map[string]any) (any, error) {
		session.Receive("thread/settings/updated", map[string]any{
			"threadId": "task", "threadSettings": map[string]any{"model": "m"},
		})
		return map[string]any{}, nil
	}
	if err := session.applySettings("task", map[string]any{"threadId": "task", "model": "m"},
		protocol.SessionSettings{Model: protocol.Known("m")}); err != nil {
		t.Fatal(err)
	}
	if host.count("thread/settings/update") != 1 {
		t.Fatal(host.methods())
	}
}
