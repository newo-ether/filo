package service

import (
	"context"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

func TestPayloadPagesAndTopologyKeepIndependentNativeReceipts(t *testing.T) {
	reader, _ := payloadFixture(160)
	confirmed := protocol.InputReceipt{ClientID: "accepted-client", TurnID: "older-turn", MessageID: "off-page-user"}
	reader.page.InputReceipts = []protocol.InputReceipt{confirmed}
	payloads := NewConversationPayloads(DefaultPayloadCacheBytes)
	page, err := payloads.Page(context.Background(), reader, payloadSession, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) >= 160 || page.NextCursor == nil {
		t.Fatal("Fixture did not paginate message bodies")
	}
	if len(page.InputReceipts) != 1 || page.InputReceipts[0] != confirmed {
		t.Fatalf("Body page lost independent native receipt: %+v", page.InputReceipts)
	}
	topology, err := payloads.Topology(context.Background(), reader, payloadSession, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.InputReceipts) != 1 || topology.InputReceipts[0] != confirmed {
		t.Fatalf("Topology lost independent native receipt: %+v", topology.InputReceipts)
	}
	for _, node := range topology.Nodes {
		if node.ID == confirmed.MessageID {
			t.Fatal("Fixture receipt must be independent of visible nodes")
		}
	}
}

func TestTaskMetadataKeepsNativeSettingsAndSupplementsPersistedTelemetry(t *testing.T) {
	model, effort, tier, completed := "live-model", "high", "fast", "completed-turn"
	tokens, window := float64(41), float64(1000)
	persistedReceipt := protocol.InputReceipt{ClientID: "persisted-client", TurnID: completed, MessageID: "persisted-user"}
	liveReceipt := protocol.InputReceipt{ClientID: "live-client", TurnID: "active-turn", MessageID: "live-user"}
	page := protocol.ConversationPage{
		Runtime: &protocol.Runtime{Status: "notLoaded", ContextTokens: &tokens, ContextWindow: &window,
			Effort: protocol.Known(&effort), ServiceTier: protocol.Known(&tier), CompletedTurnID: protocol.Known(&completed)},
		InputReceipts: []protocol.InputReceipt{persistedReceipt},
	}
	live := protocol.ConversationPage{
		Runtime:       &protocol.Runtime{Status: "idle", Model: &model, ServiceTier: protocol.Known[*string](nil)},
		InputReceipts: []protocol.InputReceipt{liveReceipt},
	}
	mergeTaskMetadata(&page, live)
	if page.Runtime.Status != "idle" || page.Runtime.Model != &model || !page.Runtime.ServiceTier.Known || page.Runtime.ServiceTier.Value != nil {
		t.Fatalf("Persisted data replaced current native settings: %+v", page.Runtime)
	}
	if page.Runtime.ContextTokens != &tokens || page.Runtime.ContextWindow != &window || page.Runtime.Effort.Value != &effort || page.Runtime.CompletedTurnID.Value != &completed {
		t.Fatalf("Persisted metadata was lost: %+v", page.Runtime)
	}
	if len(page.InputReceipts) != 2 || page.InputReceipts[0] != persistedReceipt || page.InputReceipts[1] != liveReceipt {
		t.Fatalf("Persisted and native receipt merge = %+v", page.InputReceipts)
	}
}
