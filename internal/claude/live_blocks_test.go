package claude

import (
	"context"
	"net/http"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestNativeBlocksUpdateInPlaceAndFinishWithoutInventedReceipts(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	seq := uint64(0)
	push := func(events ...modEvent) int {
		seq++
		status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: seq, Events: events})
		if status != http.StatusOK {
			seq--
		}
		return status
	}
	text := func(value string) modEvent {
		return modEvent{Kind: "block", TurnID: "native-turn", Block: &modBlock{Step: 0, Index: 0, Type: "text", Text: protocol.Text(value)}}
	}
	if push(modEvent{Kind: "start", TurnID: "native-turn", Text: "prompt"}, text("first")) != 200 {
		t.Fatal("start")
	}
	args, result := protocol.Text(`{"file_path":"fixture"}`), protocol.Text("native result")
	tool := modEvent{Kind: "block", TurnID: "native-turn", Block: &modBlock{Step: 0, Index: 1, Type: "tool", NativeID: "native-tool", ToolName: "Read", Arguments: &args, State: "running"}}
	if push(text("first second"), tool) != 200 {
		t.Fatal("update")
	}
	page, _ := a.Read(context.Background(), fixtureSession, "", true, nil)
	if len(page.Messages) != 2 || page.Messages[0].Text != "first second" || page.Messages[0].TurnID != "native-turn" || page.Messages[1].ID != "mod-tool:native-tool" {
		t.Fatal(page.Messages)
	}
	tool.Block.State, tool.Block.Result = "succeeded", &result
	if push(tool, modEvent{Kind: "complete", TurnID: "native-turn"}) != 200 {
		t.Fatal("complete")
	}
	page, _ = a.Read(context.Background(), fixtureSession, "", true, nil)
	if len(page.Messages) != 2 || page.Messages[1].Activity.State != "succeeded" || page.Messages[1].Activity.Result.Value != result || page.Runtime.ActiveTurnID != nil || len(page.InputReceipts) != 0 {
		t.Fatal(page)
	}
	// A conflicting later update rolls the whole batch back, including text.
	tool.Block.ToolName = "Bash"
	if push(text("must roll back"), tool) != 409 {
		t.Fatal("identity mutation accepted")
	}
	page, _ = a.Read(context.Background(), fixtureSession, "", true, nil)
	if page.Messages[0].Text != "first second" {
		t.Fatal("batch was not atomic")
	}
}

func TestInterruptedNativeToolDoesNotRemainRunning(t *testing.T) {
	a := fixtureAdapter(t)
	nonce := registerFixture(t, a, fixtureSession)
	events := []modEvent{{Kind: "start", TurnID: "turn", Text: "prompt"}, {Kind: "block", TurnID: "turn", Block: &modBlock{Type: "tool", NativeID: "tool", ToolName: "Read", State: "running"}}, {Kind: "complete", TurnID: "turn", Aborted: true}}
	status, _ := bridgeCall(t, a, "push", envelope{SessionID: fixtureSession, Nonce: nonce, Sequence: 1, Events: events})
	page, _ := a.Read(context.Background(), fixtureSession, "", true, nil)
	if status != 200 || page.Messages[0].Activity.State != "stopped" {
		t.Fatal(status, page)
	}
}
