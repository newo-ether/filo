package codex

import (
	"errors"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestNativeSettingsWaitForAppliedReadbackAndDoNotReplayUnknown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := newSessionHost()
		host.replies["thread/resume"] = sessionReply(map[string]any{"model": "m", "reasoningEffort": "high", "serviceTier": nil})
		host.replies["model/list"] = sessionReply(map[string]any{"data": []any{
			map[string]any{"model": "m", "isDefault": true, "supportedReasoningEfforts": []any{
				map[string]any{"reasoningEffort": "low"}, map[string]any{"reasoningEffort": "high"},
			}},
		}})
		session := NewNativeTaskSession(host)
		host.replies["thread/read"] = sessionReply(map[string]any{"thread": map[string]any{"id": "task", "model": "m"}})
		if err := session.Attach("task"); err != nil {
			t.Fatal(err)
		}
		// Empty acknowledgement leaves the old value intact and is not success.
		if err := session.UpdateSettings("task", protocol.SessionSettings{Effort: protocol.Known("low")}); err == nil {
			t.Fatal("Empty native acknowledgement confirmed desired settings")
		}
		if host.count("thread/settings/update") != 1 || *session.settings["task"].Effort != "high" {
			t.Fatal("Uncertain settings replayed or copied into readback")
		}
		// Matching cache from before this request cannot confirm application either.
		if err := session.UpdateSettings("task", protocol.SessionSettings{Effort: protocol.Known("high")}); err == nil {
			t.Fatal("Stale matching settings confirmed an empty native acknowledgement")
		}
		if host.count("thread/settings/update") != 2 {
			t.Fatal("Unconfirmed settings were replayed")
		}
		host.replies["thread/settings/update"] = func(map[string]any) (any, error) {
			session.Receive("thread/settings/updated", map[string]any{
				"threadId": "task", "threadSettings": map[string]any{"model": "m", "effort": "low", "serviceTier": nil},
			})
			return map[string]any{}, nil
		}
		if err := session.UpdateSettings("task", protocol.SessionSettings{
			Effort: protocol.Known("low"), ServiceTier: protocol.Known[*string](nil),
		}); err != nil {
			t.Fatal(err)
		}
		if host.count("turn/start") != 0 || host.count("thread/resume") != 1 {
			t.Fatal(host.methods())
		}
	})
}

func TestNativeSettingsRejectForeignReadbackAndMutationError(t *testing.T) {
	host := newSessionHost()
	host.replies["thread/start"] = sessionReply(map[string]any{"thread": map[string]any{"id": "task"}})
	host.replies["model/list"] = sessionReply(map[string]any{"data": []any{map[string]any{"model": "m"}}})
	session := NewNativeTaskSession(host)
	if _, err := session.Create(); err != nil {
		t.Fatal(err)
	}
	host.replies["thread/settings/update"] = sessionReply(map[string]any{"threadId": "foreign", "threadSettings": map[string]any{"model": "m"}})
	if err := session.UpdateSettings("task", protocol.SessionSettings{Model: protocol.Known("m")}); err == nil {
		t.Fatal("Foreign settings identity accepted")
	}
	host.replies["thread/settings/update"] = sessionFailure(errors.New("uncertain"))
	if err := session.UpdateSettings("task", protocol.SessionSettings{Model: protocol.Known("m")}); err == nil {
		t.Fatal("Mutation failure accepted")
	}
	if host.count("thread/settings/update") != 2 {
		t.Fatal(host.methods())
	}
	if readNativeSettings(map[string]any{"serviceTier": false}, "effort").ServiceTier.Known {
		t.Fatal("Malformed tier became Default")
	}
}

func TestNativeSettingsCanonicalDefaultReadback(t *testing.T) {
	host := newSessionHost()
	host.replies["thread/start"] = sessionReply(map[string]any{"thread": map[string]any{"id": "task"}, "model": "m"})
	host.replies["thread/read"] = sessionReply(map[string]any{"thread": map[string]any{"id": "task", "model": "m"}})
	host.replies["model/list"] = sessionReply(map[string]any{"data": []any{map[string]any{"model": "m"}}})
	session := NewNativeTaskSession(host)
	if _, err := session.Create(); err != nil {
		t.Fatal(err)
	}
	host.replies["thread/settings/update"] = func(params map[string]any) (any, error) {
		value, present := params["serviceTier"]
		tier, typed := value.(*string)
		if !present || !typed || tier != nil {
			t.Fatalf("Default request = %#v", params)
		}
		session.Receive("thread/settings/updated", map[string]any{
			"threadId": "task", "threadSettings": map[string]any{"model": "m", "serviceTier": "default"},
		})
		return map[string]any{}, nil
	}
	if err := session.UpdateSettings("task", protocol.SessionSettings{ServiceTier: protocol.Known[*string](nil)}); err != nil {
		t.Fatal(err)
	}
	if tier := session.settings["task"].ServiceTier; !tier.Known || tier.Value != nil {
		t.Fatalf("Canonical Default = %+v", tier)
	}
	if host.count("thread/settings/update") != 1 || host.count("turn/start") != 0 {
		t.Fatal(host.methods())
	}
	for _, native := range []any{nil, "default", "priority"} {
		tier := readNativeSettings(map[string]any{"serviceTier": native}, "reasoningEffort").ServiceTier
		if !tier.Known || (native == "priority" && (tier.Value == nil || *tier.Value != "priority")) ||
			(native != "priority" && tier.Value != nil) {
			t.Fatalf("Native tier %#v = %+v", native, tier)
		}
	}
}

func TestNativeInputReceiptsRequireConfirmedUserItemsAndStayBounded(t *testing.T) {
	host := newSessionHost()
	host.replies["thread/resume"] = sessionReply(map[string]any{})
	session := NewNativeTaskSession(host)
	if err := session.Attach("task"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 140; index++ {
		session.Receive("item/completed", map[string]any{"threadId": "task", "turnId": "turn",
			"item": map[string]any{"id": fmt.Sprint("u-", index), "type": "userMessage", "clientId": fmt.Sprint("c-", index)},
		})
	}
	session.Receive("item/completed", map[string]any{"threadId": "foreign", "turnId": "turn",
		"item": map[string]any{"id": "foreign", "type": "userMessage", "clientId": "other"},
	})
	session.Receive("item/completed", map[string]any{"threadId": "task", "turnId": "turn",
		"item": map[string]any{"id": "assistant", "type": "agentMessage", "clientId": "other"},
	})
	receipts := session.receipts["task"]
	if len(receipts) != 128 || receipts[0].ClientID != "c-12" || receipts[127].ClientID != "c-139" {
		t.Fatalf("%+v", receipts)
	}
	session.Disconnected()
	if len(session.receipts) != 0 {
		t.Fatal("Disconnected live evidence retained")
	}
}
