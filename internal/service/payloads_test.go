package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
)

const payloadSession = "11111111-1111-4111-8111-111111111111"

// payloadFixture mirrors the TS fixture: one shared page whose bodies stay
// mutable, so a test can prove the pager never rewrites native records.
func payloadFixture(count int) (*fakeReader, []protocol.Message) {
	messages := make([]protocol.Message, 0, count)
	for index := 0; index < count; index++ {
		message := protocol.Message{
			MessageIdentity: protocol.MessageIdentity{
				ID:        "m" + strconv.Itoa(index),
				TurnID:    "turn",
				Role:      "assistant",
				Timestamp: protocol.Number(index),
			},
			Text: protocol.Text("private body " + strconv.Itoa(index)),
		}
		if index == 0 {
			message.Role = "user"
		} else {
			message.Activity = &protocol.Activity{
				Type:      "tool",
				ToolName:  "shell",
				Arguments: protocol.Known(protocol.Text("private arguments")),
				Result:    protocol.Known(protocol.Text(strings.Repeat("private output", 10000))),
			}
		}
		messages = append(messages, message)
	}
	return &fakeReader{page: protocol.ConversationPage{Messages: messages, Queued: []protocol.QueuedInput{}}}, messages
}

func TestTopologyCarriesIdentityAndRevisionsWithoutBodies(t *testing.T) {
	reader, messages := payloadFixture(40)
	payloads := NewConversationPayloads(DefaultPayloadCacheBytes)
	cursor, ids, pages := "", []string{}, 0
	for {
		page, err := payloads.Topology(context.Background(), reader, payloadSession, cursor, "")
		if err != nil {
			t.Fatalf("topology = %v", err)
		}
		text := string(Marshal(page))
		for _, banned := range []string{"private", "arguments", "result"} {
			if strings.Contains(text, banned) {
				t.Fatalf("topology leaked %q: %s", banned, text)
			}
		}
		pageIDs := make([]string, 0, len(page.Nodes))
		for _, node := range page.Nodes {
			if !revisionPattern.MatchString(node.Revision) {
				t.Fatalf("revision = %q", node.Revision)
			}
			pageIDs = append(pageIDs, node.ID)
		}
		ids = append(pageIDs, ids...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
		if pages++; pages > 40 {
			t.Fatal("pagination must advance")
		}
	}
	assertSameIDs(t, ids, messages)
}

func TestEvictedPayloadsRehydrateWithoutChangingTopology(t *testing.T) {
	reader, messages := payloadFixture(3)
	payloads := NewConversationPayloads(1)
	page, err := payloads.Topology(context.Background(), reader, payloadSession, "", "")
	if err != nil {
		t.Fatalf("topology = %v", err)
	}
	if payloads.RetainedBytes() != 0 {
		t.Fatalf("retainedBytes = %d, want 0", payloads.RetainedBytes())
	}
	node := page.Nodes[0]
	loaded, err := payloads.Load(context.Background(), reader, payloadSession,
		[]PayloadRequest{{ID: node.ID, Revision: node.Revision}}, "", false)
	if err != nil {
		t.Fatalf("load = %v", err)
	}
	if text := string(loaded.Messages[0].Text); text != string(messages[0].Text) {
		t.Fatalf("loaded text = %q, want %q", text, messages[0].Text)
	}
	again, err := payloads.Topology(context.Background(), reader, payloadSession, "", "")
	if err != nil {
		t.Fatalf("second topology = %v", err)
	}
	if string(Marshal(again.Nodes)) != string(Marshal(page.Nodes)) {
		t.Fatalf("topology changed: %s", Marshal(again.Nodes))
	}
	if messages[1].Activity == nil || len(string(messages[1].Activity.Result.Value)) != len(strings.Repeat("private output", 10000)) {
		t.Fatal("native record was rewritten")
	}
	if messages[0].GroupID != "" {
		t.Fatalf("source groupId = %q, want unset", messages[0].GroupID)
	}
}

func TestCachedReadsAreTaskBoundAndRevisionsInvalidateIndividually(t *testing.T) {
	reader, messages := payloadFixture(2)
	payloads := NewConversationPayloads(DefaultPayloadCacheBytes)
	first, err := payloads.Topology(context.Background(), reader, payloadSession, "", "")
	if err != nil {
		t.Fatalf("topology = %v", err)
	}
	before := reader.reads
	loaded, err := payloads.Load(context.Background(), reader, payloadSession,
		[]PayloadRequest{{ID: first.Nodes[0].ID, Revision: first.Nodes[0].Revision}}, "", false)
	if err != nil {
		t.Fatalf("load = %v", err)
	}
	if loaded.Messages[0].ID != "m0" {
		t.Fatalf("loaded %q", loaded.Messages[0].ID)
	}
	if reader.reads != before {
		t.Fatalf("reads = %d, want %d", reader.reads, before)
	}
	messages[0].Text = protocol.Text("changed")
	second, err := payloads.Topology(context.Background(), reader, payloadSession, "", "")
	if err != nil {
		t.Fatalf("second topology = %v", err)
	}
	if first.Nodes[0].Revision == second.Nodes[0].Revision {
		t.Fatal("changed content kept its revision")
	}
	if first.Nodes[1].Revision != second.Nodes[1].Revision {
		t.Fatal("unchanged content lost its revision")
	}
	reloaded, err := payloads.Load(context.Background(), reader, payloadSession,
		[]PayloadRequest{{ID: second.Nodes[0].ID, Revision: second.Nodes[0].Revision}}, "", false)
	if err != nil {
		t.Fatalf("reload = %v", err)
	}
	if text := string(reloaded.Messages[0].Text); text != "changed" {
		t.Fatalf("reloaded text = %q", text)
	}
	_, err = payloads.Load(context.Background(), reader, payloadSession, nil, "", false)
	assertRpcRefusal(t, err, "Expected 1-3 distinct message identities and revisions", -32602)
}

func TestExactRevisionRefusesAChangedBody(t *testing.T) {
	reader, _ := payloadFixture(2)
	payloads := NewConversationPayloads(DefaultPayloadCacheBytes)
	_, err := payloads.Load(context.Background(), reader, payloadSession,
		[]PayloadRequest{{ID: "m0", Revision: strings.Repeat("a", 64)}}, "", true)
	assertRpcRefusal(t, err, "Image changed; reload its message", -32600)
}

func TestPayloadRequestsAreBoundedAndDistinct(t *testing.T) {
	reader, _ := payloadFixture(2)
	payloads := NewConversationPayloads(DefaultPayloadCacheBytes)
	revision := strings.Repeat("a", 64)
	cases := [][]PayloadRequest{
		{{ID: "m0", Revision: revision}, {ID: "m0", Revision: revision}},
		{{ID: "", Revision: revision}},
		{{ID: strings.Repeat("i", 513), Revision: revision}},
		{{ID: "m0", Revision: strings.ToUpper(revision)}},
		{{ID: "m0", Revision: revision}, {ID: "m1", Revision: revision}, {ID: "m3", Revision: revision}, {ID: "m4", Revision: revision}},
	}
	for _, requests := range cases {
		_, err := payloads.Load(context.Background(), reader, payloadSession, requests, "", false)
		assertRpcRefusal(t, err, "Expected 1-3 distinct message identities and revisions", -32602)
	}
}

func TestBodyPageIncludesImageRevisionsInOneBoundedRead(t *testing.T) {
	reader, _ := payloadFixture(3000)
	payloads := NewConversationPayloads(DefaultPayloadCacheBytes)
	page, err := payloads.Page(context.Background(), reader, payloadSession, "", "", nil)
	if err != nil {
		t.Fatalf("page = %v", err)
	}
	if reader.reads != 1 {
		t.Fatalf("reads = %d, want 1", reader.reads)
	}
	if page.NextCursor == nil {
		t.Fatal("page must keep a next cursor")
	}
	if len(page.Messages) == 0 || len(page.Messages) >= 128 {
		t.Fatalf("window = %d messages", len(page.Messages))
	}
	if len(page.Nodes) != len(page.Messages) {
		t.Fatalf("nodes = %d, messages = %d", len(page.Nodes), len(page.Messages))
	}
	for index := range page.Messages {
		if page.Nodes[index].ID != page.Messages[index].ID {
			t.Fatalf("node %d = %q, message = %q", index, page.Nodes[index].ID, page.Messages[index].ID)
		}
	}
	if size := Size(page); size >= 1024*1024 {
		t.Fatalf("page serialized to %d bytes", size)
	}
	node := page.Nodes[0]
	loaded, err := payloads.Load(context.Background(), reader, payloadSession,
		[]PayloadRequest{{ID: node.ID, Revision: node.Revision}}, "", false)
	if err != nil {
		t.Fatalf("load = %v", err)
	}
	if loaded.Messages[0].ID != node.ID {
		t.Fatalf("loaded %q, want %q", loaded.Messages[0].ID, node.ID)
	}
	if reader.reads != 1 {
		t.Fatalf("reads = %d, want the cache hit to avoid a native read", reader.reads)
	}
}

func TestPhysicalPacketsBudgetNodesAcrossNestedLayers(t *testing.T) {
	messages := make([]protocol.Message, 0, 240)
	for index := 0; index < 240; index++ {
		id := "message-" + strconv.Itoa(index) + strings.Repeat("-", 120)
		messages = append(messages, protocol.Message{
			MessageIdentity: protocol.MessageIdentity{
				ID:        id,
				TurnID:    payloadSession,
				Role:      "assistant",
				GroupID:   "group-" + payloadSession,
				Timestamp: protocol.Number(index),
			},
			Text:     protocol.Text(strings.Repeat("x", 1400)),
			Activity: &protocol.Activity{Type: "thought"},
		})
	}
	original := &fakeReader{page: protocol.ConversationPage{Messages: messages, Queued: []protocol.QueuedInput{}}}
	for _, nested := range []bool{false, true} {
		var source ConversationReader = original
		if nested {
			source = readerFunc(func(ctx context.Context, id, cursor string, activity bool,
				excluded []string) (protocol.ConversationPage, error) {
				return ReadConversationPage(ctx, original, id, cursor, PageOptions{
					Activity: activity, Layer: "worker", ExcludedTurns: excluded,
				})
			})
		}
		payloads := NewConversationPayloads(DefaultPayloadCacheBytes)
		cursor, ids, count := "", []string{}, 0
		for {
			page, err := payloads.Page(context.Background(), source, payloadSession, cursor, "", nil)
			if err != nil {
				t.Fatalf("page = %v", err)
			}
			if size := Size(page); size > MaxConversationPageBytes {
				t.Fatalf("page serialized to %d bytes", size)
			}
			if len(page.Messages) == 0 || len(page.Messages) > 128 {
				t.Fatalf("window = %d messages", len(page.Messages))
			}
			for index := range page.Messages {
				if page.Nodes[index].ID != page.Messages[index].ID {
					t.Fatalf("node %d = %q, message = %q", index, page.Nodes[index].ID, page.Messages[index].ID)
				}
			}
			if !page.PageCursor.Known {
				t.Fatal("page must carry a re-read cursor")
			}
			replay, err := payloads.Page(context.Background(), source, payloadSession, page.PageCursor.Value, "", nil)
			if err != nil {
				t.Fatalf("replay = %v", err)
			}
			if string(Marshal(replay.Messages)) != string(Marshal(page.Messages)) ||
				string(Marshal(replay.Nodes)) != string(Marshal(page.Nodes)) {
				t.Fatal("replay changed the window")
			}
			pageIDs := messageIDs(page.Messages)
			ids = append(pageIDs, ids...)
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
			if count++; count > 20 {
				t.Fatal("pagination must advance")
			}
		}
		if count <= 1 {
			t.Fatalf("nested=%v produced %d pages", nested, count)
		}
		assertSameIDs(t, ids, messages)
	}
}

func TestPayloadCursorLoopIsRefused(t *testing.T) {
	reader := readerFunc(func(_ context.Context, _ string, _ string, _ bool,
		_ []string) (protocol.ConversationPage, error) {
		stuck := "same-cursor"
		return protocol.ConversationPage{Messages: []protocol.Message{}, Queued: []protocol.QueuedInput{}, NextCursor: &stuck}, nil
	})
	payloads := NewConversationPayloads(DefaultPayloadCacheBytes)
	_, err := payloads.Load(context.Background(), reader, payloadSession,
		[]PayloadRequest{{ID: "m0", Revision: strings.Repeat("a", 64)}}, "", false)
	if err == nil || !strings.Contains(err.Error(), "Filo history cursor did not advance") {
		t.Fatalf("error = %v", err)
	}
	var rpc *codex.RpcError
	if errors.As(err, &rpc) {
		t.Fatalf("error = %v, want a plain error", err)
	}
}
