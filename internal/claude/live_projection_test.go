package claude

import (
	"context"
	"net/http"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestNativeActiveInputAndTitleEvidence(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	seq := uint64(0)
	push := func(events ...modEvent) {
		t.Helper()
		seq++
		status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: seq, Events: events})
		if status != http.StatusOK {
			t.Fatalf("push status %d", status)
		}
	}
	title := protocol.Text("Native title")
	push(modEvent{Kind: "start", TurnID: "turn", Text: "Actual native prompt"}, modEvent{Kind: "title", Title: &title})
	page, err := a.Read(context.Background(), fixtureSession, "", true, nil)
	if err != nil || !page.Runtime.ActiveTurnHasUserMessage.Value || page.Runtime.Title == nil || *page.Runtime.Title != title {
		t.Fatalf("missing native evidence: %+v %v", page.Runtime, err)
	}
	if len(page.Messages) != 0 || len(page.InputReceipts) != 0 {
		t.Fatal("turn evidence invented a row or receipt")
	}
	listed, _ := a.List(context.Background(), "")
	if listed.Sessions[0].Title != title {
		t.Fatal("catalog title differs")
	}
	push(modEvent{Kind: "truncated"})
	page, _ = a.Read(context.Background(), fixtureSession, "", true, nil)
	if page.Runtime.Status != "active" || page.Runtime.ActiveTurnID == nil || !page.Runtime.ActiveTurnHasUserMessage.Value {
		t.Fatal("bounded content truncation cleared authoritative native generation")
	}
	push(modEvent{Kind: "complete", TurnID: "turn", Aborted: true})
	page, _ = a.Read(context.Background(), fixtureSession, "", true, nil)
	if page.Runtime.ActiveTurnHasUserMessage.Value || page.Runtime.ActiveTurnID != nil || page.Runtime.Status != "idle" {
		t.Fatal("terminal did not clear generation")
	}
	push(modEvent{Kind: "start", TurnID: "continuation"})
	page, _ = a.Read(context.Background(), fixtureSession, "", true, nil)
	if page.Runtime.ActiveTurnHasUserMessage.Value {
		t.Fatal("empty continuation invented a user prompt")
	}
	push(modEvent{Kind: "gap", Dropped: true})
	page, _ = a.Read(context.Background(), fixtureSession, "", true, nil)
	if page.Runtime.Status != "unknown" {
		t.Fatal("lost native events retained unsupported lifecycle authority")
	}
}
