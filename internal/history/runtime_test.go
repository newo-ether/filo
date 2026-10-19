package history

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestRetiredNativeRuntimeReadsVerifiedSettingsUsageAndTerminal(t *testing.T) {
	f := nativeHistoryFixture(t, 1, 0)
	appendRollout(t, f.path, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"task","thread_settings":{"model":"selected-model","reasoning_effort":"xhigh","service_tier":null}}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":4321},"model_context_window":128000}}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"native-turn"}}`+"\n")
	before := fileHash(t, f.path)
	page := f.read(t, "")
	runtime := page.Runtime
	if runtime.Status != "idle" || runtime.ActiveTurnID != nil || !runtime.CompletedTurnID.Known || runtime.CompletedTurnID.Value == nil || *runtime.CompletedTurnID.Value != "native-turn" {
		t.Fatalf("terminal runtime = %+v", runtime)
	}
	if runtime.Model == nil || *runtime.Model != "selected-model" || runtime.Effort.Value == nil || *runtime.Effort.Value != "xhigh" || !runtime.ServiceTier.Known || runtime.ServiceTier.Value != nil || !runtime.ServiceTierKnown.Value {
		t.Fatalf("settings = %+v", runtime)
	}
	if runtime.ContextTokens == nil || *runtime.ContextTokens != 4321 || runtime.ContextWindow == nil || *runtime.ContextWindow != 128000 {
		t.Fatalf("usage = %+v", runtime)
	}
	if fileHash(t, f.path) != before {
		t.Fatal("Reading metadata rewrote native history")
	}
}

func TestNewerNativeStartDoesNotExposeAnOlderCompletion(t *testing.T) {
	f := nativeHistoryFixture(t, 1, 0)
	f.write(t, History, func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO thread_turns VALUES('task','old-turn','completed',NULL,1,1)`); err != nil {
			t.Fatal(err)
		}
	})
	appendRollout(t, f.path, `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"old-turn"}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"new-turn"}}`+"\n")
	page := f.read(t, "")
	if page.Runtime.Status != "notLoaded" || !page.Runtime.CompletedTurnID.Known || page.Runtime.CompletedTurnID.Value != nil || page.Runtime.ActiveTurnID != nil {
		t.Fatalf("New start was settled by an older completion: %+v", page.Runtime)
	}
}

func TestRetiredReceiptsRemainIndependentOfVisiblePageAndIndexCatchup(t *testing.T) {
	f := nativeHistoryFixture(t, 1, 0)
	appendRollout(t, f.path, f.event(1, map[string]any{"id": "native-user", "type": "UserMessage", "client_id": "accepted-client", "content": []any{}}))
	for index := 2; index < 42; index++ {
		appendRollout(t, f.path, f.event(index, nil))
	}
	page := f.read(t, "")
	if len(page.InputReceipts) != 1 || page.InputReceipts[0].ClientID != "accepted-client" || page.InputReceipts[0].MessageID != "native-user" || page.InputReceipts[0].TurnID != "new-turn" {
		t.Fatalf("persisted receipt = %+v", page.InputReceipts)
	}
	for _, message := range page.Messages {
		if message.ID == "native-user" {
			t.Fatal("Fixture did not move the accepted user item outside the visible page")
		}
	}
	// Independently indexed evidence also works when the bounded rollout tail
	// no longer contains that older accepted input.
	f.write(t, History, func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO thread_items VALUES('task','indexed-turn','indexed-user',-1,0,'{"id":"indexed-user","type":"userMessage","clientId":"indexed-client","content":[]}')`); err != nil {
			t.Fatal(err)
		}
	})
	page = f.read(t, "")
	if len(page.InputReceipts) != 2 {
		t.Fatalf("indexed and rollout receipts = %+v", page.InputReceipts)
	}
}

func TestRetiredRuntimeKeepsUnknownAndBoundsReceipts(t *testing.T) {
	f := nativeHistoryFixture(t, 0, 0)
	for index := 0; index < 140; index++ {
		appendRollout(t, f.path, f.event(index, map[string]any{"id": fmt.Sprint("user-", index), "type": "UserMessage", "client_id": fmt.Sprint("client-", index), "content": []any{}}))
	}
	page := f.read(t, "")
	if len(page.InputReceipts) != 128 || page.InputReceipts[0].ClientID != "client-12" || page.InputReceipts[127].ClientID != "client-139" {
		t.Fatalf("bounded receipts = %d %+v", len(page.InputReceipts), page.InputReceipts)
	}
	if page.Runtime.ContextTokens != nil || page.Runtime.ServiceTier.Known || page.Runtime.CompletedTurnID.Known {
		t.Fatalf("Unreported runtime metadata was invented: %+v", page.Runtime)
	}
	if _, err := ReadRuntime(context.Background(), f.path, "foreign", nil, nil); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("foreign runtime identity = %v", err)
	}
}
