package codex

import (
	"fmt"
	"testing"
)

func TestDesktopReceiptsSurviveVisibleTurnLimitAndRejectUnacceptedSteers(t *testing.T) {
	state := desktopFixture()
	entityOf(t, state, "a")["items"].([]any)[0].(map[string]any)["clientId"] = "off-page-client"
	live := entityOf(t, state, "b")
	live["items"] = append(live["items"].([]any),
		map[string]any{"type": "steeringUserMessage", "id": "local-pending", "clientUserMessageId": "pending", "status": "pending"},
		map[string]any{"type": "steeringUserMessage", "id": "local-rejected", "clientUserMessageId": "rejected", "status": "rejected"},
		map[string]any{"type": "steeringUserMessage", "id": "local-accepted", "serverUserMessageId": "native-steer", "clientUserMessageId": "accepted", "status": "accepted"},
	)
	page, err := ProjectDesktop(state, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Messages {
		if message.ID == "u1" {
			t.Fatal("Fixture did not put the earlier user message outside the visible window")
		}
	}
	if len(page.InputReceipts) != 3 {
		t.Fatalf("Native receipt identities = %+v", page.InputReceipts)
	}
	if receipt := page.InputReceipts[0]; receipt.ClientID != "off-page-client" || receipt.TurnID != "old" || receipt.MessageID != "u1" {
		t.Fatalf("Off-page native receipt = %+v", receipt)
	}
	if receipt := page.InputReceipts[2]; receipt.ClientID != "accepted" || receipt.TurnID != "live" || receipt.MessageID != "native-steer" {
		t.Fatalf("Accepted steer receipt = %+v", receipt)
	}
}

func TestDesktopReceiptsBoundNewestAndOmitConflictingIdentity(t *testing.T) {
	state := desktopFixture()
	old := entityOf(t, state, "a")
	old["items"] = []any{map[string]any{"type": "userMessage", "id": "conflicting-user", "clientId": "client-139"}}
	live := entityOf(t, state, "b")
	items := make([]any, 0, 140)
	for index := 0; index < 140; index++ {
		items = append(items, map[string]any{"type": "userMessage", "id": fmt.Sprint("user-", index), "clientId": fmt.Sprint("client-", index)})
	}
	live["items"] = items
	page, err := ProjectDesktop(state, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.InputReceipts) != 127 || page.InputReceipts[0].ClientID != "client-12" || page.InputReceipts[126].ClientID != "client-138" {
		t.Fatalf("Bounded non-conflicting receipts = %+v", page.InputReceipts)
	}
}
