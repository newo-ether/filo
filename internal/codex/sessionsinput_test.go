package codex

import (
	"errors"
	"reflect"
	"testing"
)

func TestNativeStartTurnCarriesConfirmedTierWithoutReplay(t *testing.T) {
	for _, tier := range []any{nil, "default", "priority", false} {
		t.Run(tierName(tier), func(t *testing.T) {
			host := newSessionHost()
			host.replies["thread/resume"] = sessionReply(map[string]any{"serviceTier": tier})
			session := NewNativeTaskSession(host)
			if err := session.Attach("task"); err != nil {
				t.Fatal(err)
			}
			input := []any{map[string]any{"type": "text", "text": "hello"}}
			failure := errors.New("uncertain turn outcome")
			host.replies["turn/start"] = func(params map[string]any) (any, error) {
				if params["threadId"] != "task" || params["clientUserMessageId"] != "client" || !reflect.DeepEqual(params["input"], input) {
					t.Fatalf("turn input = %#v", params)
				}
				value, present := params["serviceTier"]
				if tier == false {
					if present {
						t.Fatal("Unknown tier became Default")
					}
				} else {
					actual, typed := value.(*string)
					if !present || !typed || (tier == "priority" && (actual == nil || *actual != "priority")) ||
						(tier != "priority" && actual != nil) {
						t.Fatalf("turn tier for %#v = %#v", tier, value)
					}
				}
				return nil, failure
			}
			if _, err := session.StartTurn("task", input, "client"); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if host.count("turn/start") != 1 {
				t.Fatal("An uncertain turn was replayed")
			}
		})
	}
}

func tierName(value any) string {
	if value == nil {
		return "null"
	}
	if text, ok := value.(string); ok {
		return text
	}
	return "unknown"
}
