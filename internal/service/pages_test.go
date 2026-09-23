package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
	"github.com/newo-ether/filo/internal/toolpreview"
)

type fakeReader struct {
	page         protocol.ConversationPage
	reads        int
	lastID       string
	lastCursor   string
	lastActivity bool
	onRead       func()
}

func (reader *fakeReader) Read(_ context.Context, id, cursor string, activity bool,
	_ []string) (protocol.ConversationPage, error) {
	reader.reads++
	reader.lastID, reader.lastCursor, reader.lastActivity = id, cursor, activity
	if reader.onRead != nil {
		reader.onRead()
	}
	return reader.page, nil
}

// readerFunc lets a test compose one pager in front of another, the way the
// worker layer wraps the desktop layer in TS.
type readerFunc func(context.Context, string, string, bool, []string) (protocol.ConversationPage, error)

func (f readerFunc) Read(ctx context.Context, id, cursor string, activity bool,
	excluded []string) (protocol.ConversationPage, error) {
	return f(ctx, id, cursor, activity, excluded)
}

func pagesRuntime() *protocol.Runtime {
	turn, model := "turn", "model"
	return &protocol.Runtime{Status: "active", ActiveTurnID: &turn, Model: &model}
}

func newPagesFixture(count int) ([]protocol.Message, *fakeReader) {
	result := strings.Repeat("\u4f60\u597d \U0001F30D", 20000)
	messages := make([]protocol.Message, 0, count)
	for index := 0; index < count; index++ {
		message := protocol.Message{
			MessageIdentity: protocol.MessageIdentity{
				ID:        "m" + strconv.Itoa(index),
				TurnID:    "turn",
				Role:      "assistant",
				Timestamp: protocol.Number(1),
			},
		}
		if index == 0 {
			message.Role = "user"
			message.Text = protocol.Text("Native input")
		} else {
			message.Activity = &protocol.Activity{
				Type:      "tool",
				ToolName:  "shell",
				Arguments: protocol.Known(protocol.Text(`{"command":"test"}`)),
				Result:    protocol.Known(protocol.Text(result)),
			}
		}
		messages = append(messages, message)
	}
	return messages, &fakeReader{page: protocol.ConversationPage{
		Messages: messages,
		Queued:   []protocol.QueuedInput{},
		Runtime:  pagesRuntime(),
	}}
}

func messageIDs(messages []protocol.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}

func assertSameIDs(t *testing.T, got []string, want []protocol.Message) {
	t.Helper()
	expected := messageIDs(want)
	if len(got) != len(expected) {
		t.Fatalf("collected %d ids, want %d", len(got), len(expected))
	}
	for index := range expected {
		if got[index] != expected[index] {
			t.Fatalf("id %d = %q, want %q", index, got[index], expected[index])
		}
	}
}

func assertRpcRefusal(t *testing.T, err error, message string, code float64) {
	t.Helper()
	var rpc *codex.RpcError
	if !errors.As(err, &rpc) {
		t.Fatalf("error = %v, want %q", err, message)
	}
	if rpc.Message != message || rpc.Code == nil || *rpc.Code != code {
		t.Fatalf("error = %q/%v, want %q/%v", rpc.Message, rpc.Code, message, code)
	}
}

func TestHugeNativeTurnPagesWithinByteLimits(t *testing.T) {
	messages, reader := newPagesFixture(350)
	var ids []string
	cursor, pages := "", 0
	for {
		page, err := ReadConversationPage(context.Background(), reader, "thread", cursor, PageOptions{Activity: true})
		if err != nil {
			t.Fatalf("read = %v", err)
		}
		text, err := EncodeResponse(page)
		if err != nil {
			t.Fatalf("encode = %v", err)
		}
		if len(text) > MaxConversationPageBytes {
			t.Fatalf("page serialized to %d bytes", len(text))
		}
		if len(page.Messages) == 0 || len(page.Messages) > 128 {
			t.Fatalf("window = %d messages", len(page.Messages))
		}
		if page.Runtime == nil || !page.Runtime.ActiveTurnHasUserMessage.Value {
			t.Fatalf("activeTurnHasUserMessage = %+v", page.Runtime)
		}
		if !page.Runtime.ServiceTierKnown.Known || page.Runtime.ServiceTierKnown.Value {
			t.Fatalf("serviceTierKnown = %+v, want a known false", page.Runtime.ServiceTierKnown)
		}
		for _, message := range page.Messages {
			if message.Role != "assistant" {
				continue
			}
			if message.GroupID != "remote-group:turn:m0" {
				t.Fatalf("groupId = %q, want remote-group:turn:m0", message.GroupID)
			}
			if message.Activity == nil ||
				!strings.Contains(string(message.Activity.Result.Value), "Filo preview truncated") {
				t.Fatalf("activity result was not bounded: %+v", message.Activity)
			}
		}
		ids = append(messageIDs(page.Messages), ids...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
		if pages++; pages > 350 {
			t.Fatal("pagination must advance")
		}
	}
	assertSameIDs(t, ids, messages)
	if !reader.lastActivity || reader.lastID != "thread" {
		t.Fatalf("read was called with %q/%v", reader.lastID, reader.lastActivity)
	}
	if pages < 2 || reader.reads != pages+1 {
		t.Fatalf("reads = %d over %d pages, want several pages", reader.reads, pages)
	}
	if got, want := len(string(messages[1].Activity.Result.Value)), len(strings.Repeat("\u4f60\u597d \U0001F30D", 20000)); got != want {
		t.Fatalf("source result length = %d, want %d", got, want)
	}
	if messages[1].GroupID != "" {
		t.Fatalf("source message was mutated: groupId %q", messages[1].GroupID)
	}
}

func TestBoundedPacketsDivideFoldGroupsWithoutLosingRecords(t *testing.T) {
	messages, reader := newPagesFixture(350)
	plain := func(id string) protocol.Message {
		return protocol.Message{MessageIdentity: protocol.MessageIdentity{
			ID: id, TurnID: "turn", Role: "assistant", Timestamp: protocol.Number(1),
		}, Text: protocol.Text(id)}
	}
	messages = append(messages[:1], append([]protocol.Message{plain("earlier-answer")}, messages[1:]...)...)
	messages = append(messages, plain("final-answer"))
	reader.page.Messages = messages
	for _, nested := range []bool{false, true} {
		var source ConversationReader = reader
		if nested {
			source = readerFunc(func(ctx context.Context, id, cursor string, activity bool,
				excluded []string) (protocol.ConversationPage, error) {
				return ReadConversationPage(ctx, reader, id, cursor, PageOptions{
					Activity: activity, Layer: "worker", ExcludedTurns: excluded,
				})
			})
		}
		tail, err := ReadConversationPage(context.Background(), source, "thread", "", PageOptions{})
		if err != nil {
			t.Fatalf("tail = %v", err)
		}
		if last := tail.Messages[len(tail.Messages)-1].ID; last != "final-answer" {
			t.Fatalf("tail ends at %q, want final-answer", last)
		}
		hasActivity := false
		for _, message := range tail.Messages {
			if message.Activity != nil {
				hasActivity = true
			}
		}
		if !hasActivity {
			t.Fatal("a partial fold withheld every activity record")
		}
		cursor := tail.NextCursor
		ids := messageIDs(tail.Messages)
		packets := 0
		var previous []protocol.Message
		for cursor != nil {
			page, err := ReadConversationPage(context.Background(), source, "thread", *cursor, PageOptions{})
			if err != nil {
				t.Fatalf("page = %v", err)
			}
			text, err := EncodeResponse(page)
			if err != nil || len(text) > MaxConversationPageBytes {
				t.Fatalf("page encoding = %v/%d bytes", err, len(text))
			}
			if len(page.Messages) > 128 {
				t.Fatalf("window = %d messages", len(page.Messages))
			}
			revisit, err := ReadConversationPage(context.Background(), source, "thread", page.PageCursor.Value, PageOptions{})
			if err != nil {
				t.Fatalf("revisit = %v", err)
			}
			if got, want := strings.Join(messageIDs(revisit.Messages), ","), strings.Join(messageIDs(page.Messages), ","); got != want {
				t.Fatalf("revisit = %q, want %q", got, want)
			}
			ids = append(messageIDs(page.Messages), ids...)
			previous = page.Messages
			cursor = page.NextCursor
			if packets++; packets > 100 {
				t.Fatal("pagination must advance")
			}
		}
		_ = previous
		if packets <= 1 {
			t.Fatalf("packets = %d, want more than one", packets)
		}
		assertSameIDs(t, ids, messages)
	}
}

func TestWorkerLayerCursorsComposeWithoutRepetition(t *testing.T) {
	messages, reader := newPagesFixture(35)
	worker := readerFunc(func(ctx context.Context, id, cursor string, activity bool,
		excluded []string) (protocol.ConversationPage, error) {
		return ReadConversationPage(ctx, reader, id, cursor, PageOptions{
			Activity: activity, Layer: "worker", ExcludedTurns: excluded,
		})
	})
	var ids []string
	cursor, count := "", 0
	for {
		page, err := ReadConversationPage(context.Background(), worker, "thread", cursor, PageOptions{Activity: true})
		if err != nil {
			t.Fatalf("read = %v", err)
		}
		ids = append(messageIDs(page.Messages), ids...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
		if count++; count > 40 {
			t.Fatal("pagination must advance")
		}
	}
	assertSameIDs(t, ids, messages)
}

func TestCursorsAreBoundToTheirTaskAndFailClosed(t *testing.T) {
	_, reader := newPagesFixture(35)
	first, err := ReadConversationPage(context.Background(), reader, "thread", "", PageOptions{Activity: true})
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first page = %v/%v", err, first.NextCursor)
	}
	_, err = ReadConversationPage(context.Background(), reader, "other", *first.NextCursor, PageOptions{Activity: true})
	assertRpcRefusal(t, err, "Invalid history cursor", -32602)
	reader.page.Messages = nil
	_, err = ReadConversationPage(context.Background(), reader, "thread", *first.NextCursor, PageOptions{Activity: true})
	assertRpcRefusal(t, err, "History changed; reopen this session before paging", -32600)
	_, err = ReadConversationPage(context.Background(), reader, "thread", "filo.page.desktop.invalid", PageOptions{})
	assertRpcRefusal(t, err, "Invalid history cursor", -32602)
}

func TestSingleHugeUnicodeMessageStaysEnterable(t *testing.T) {
	text := strings.Repeat("line \U0001F600 \u4f60\u597d\n\u0001", 120000)
	source := &fakeReader{page: protocol.ConversationPage{
		Messages: []protocol.Message{{MessageIdentity: protocol.MessageIdentity{
			ID: "big", TurnID: "turn", Role: "assistant", Timestamp: protocol.Number(1),
		}, Text: protocol.Text(text)}},
		Queued: []protocol.QueuedInput{},
	}}
	var parts []protocol.Message
	cursor, pages := "", 0
	var revisit string
	var revisitIDs []string
	for {
		page, err := ReadConversationPage(context.Background(), source, "thread", cursor, PageOptions{})
		if err != nil {
			t.Fatalf("read = %v", err)
		}
		encoded, err := EncodeResponse(page)
		if err != nil || len(encoded) > MaxConversationPageBytes {
			t.Fatalf("page encoding = %v/%d bytes", err, len(encoded))
		}
		if len(page.Messages) == 0 || !page.PageCursor.Known {
			t.Fatalf("page = %d messages, pageCursor known = %v", len(page.Messages), page.PageCursor.Known)
		}
		for _, part := range page.Messages {
			if part.NativeID != "big" {
				t.Fatalf("nativeId = %q, want big", part.NativeID)
			}
			if !part.TextOffset.Known {
				t.Fatal("part lost its text offset")
			}
			value := string(part.Text)
			if startsWithLowSurrogate(value) || endsWithHighSurrogate(value) {
				t.Fatalf("part %q splits a surrogate pair", part.ID)
			}
		}
		if pages++; pages == 2 {
			revisit = page.PageCursor.Value
			revisitIDs = messageIDs(page.Messages)
		}
		parts = append(page.Messages, parts...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
		if pages > 100 {
			t.Fatal("pagination must advance")
		}
	}
	joined := strings.Builder{}
	for _, part := range parts {
		joined.WriteString(string(part.Text))
	}
	if joined.String() != text {
		t.Fatal("reassembled parts differ from the stored text")
	}
	if source.page.Messages[0].Text != protocol.Text(text) {
		t.Fatal("the source message was modified")
	}
	seen := map[string]bool{}
	for _, part := range parts {
		if seen[part.ID] {
			t.Fatalf("duplicate part id %q", part.ID)
		}
		seen[part.ID] = true
	}
	revisited, err := ReadConversationPage(context.Background(), source, "thread", revisit, PageOptions{})
	if err != nil {
		t.Fatalf("revisit = %v", err)
	}
	if got := strings.Join(messageIDs(revisited.Messages), ","); got != strings.Join(revisitIDs, ",") {
		t.Fatalf("revisit ids = %q, want %q", got, strings.Join(revisitIDs, ","))
	}
	if _, err := EncodeResponse(map[string]any{"text": strings.Repeat("x", MaxResponseBytes)}); err == nil {
		t.Fatal("an oversized response was encoded")
	}
}

func TestLongTextPartsSurviveDesktopWorkerNesting(t *testing.T) {
	text := strings.Repeat("\u4e00\U0001F600", 500000)
	source := &fakeReader{page: protocol.ConversationPage{
		Messages: []protocol.Message{{MessageIdentity: protocol.MessageIdentity{
			ID: "long", TurnID: "turn", Role: "user", Timestamp: protocol.Number(1),
		}, Text: protocol.Text(text)}},
		Queued: []protocol.QueuedInput{},
	}}
	worker := readerFunc(func(ctx context.Context, id, cursor string, activity bool,
		excluded []string) (protocol.ConversationPage, error) {
		return ReadConversationPage(ctx, source, id, cursor, PageOptions{
			Activity: activity, Layer: "worker", ExcludedTurns: excluded,
		})
	})
	var parts []protocol.Message
	cursor, count := "", 0
	for {
		page, err := ReadConversationPage(context.Background(), worker, "thread", cursor, PageOptions{})
		if err != nil {
			t.Fatalf("read = %v", err)
		}
		encoded, err := EncodeResponse(page)
		if err != nil || len(encoded) > MaxConversationPageBytes {
			t.Fatalf("page encoding = %v/%d bytes", err, len(encoded))
		}
		revisit, err := ReadConversationPage(context.Background(), worker, "thread", page.PageCursor.Value, PageOptions{})
		if err != nil {
			t.Fatalf("revisit = %v", err)
		}
		if got, want := strings.Join(messageIDs(revisit.Messages), ","), strings.Join(messageIDs(page.Messages), ","); got != want {
			t.Fatalf("revisit ids = %q, want %q", got, want)
		}
		parts = append(page.Messages, parts...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
		if count++; count > 100 {
			t.Fatal("pagination must advance")
		}
	}
	joined := strings.Builder{}
	seen := map[string]bool{}
	for _, part := range parts {
		joined.WriteString(string(part.Text))
		if seen[part.ID] {
			t.Fatalf("duplicate part id %q", part.ID)
		}
		seen[part.ID] = true
	}
	if joined.String() != text {
		t.Fatal("reassembled parts differ from the stored text")
	}
}

// startsWithLowSurrogate and endsWithHighSurrogate read the WTF-8 form the
// native decoder preserves for unpaired surrogates.
func startsWithLowSurrogate(value string) bool {
	return len(value) >= 3 && value[0] == 0xED && value[1] >= 0xB0 && value[1] <= 0xBF && value[2]&0xC0 == 0x80
}

func endsWithHighSurrogate(value string) bool {
	size := len(value)
	return size >= 3 && value[size-3] == 0xED && value[size-2] >= 0xA0 && value[size-2] <= 0xAF && value[size-1]&0xC0 == 0x80
}

func TestPartTextRangeMatchesUTF16Slicing(t *testing.T) {
	text := strings.Repeat("\u4e00\U0001F600", 9000)
	parts := splitTextParts([]protocol.Message{{MessageIdentity: protocol.MessageIdentity{
		ID: "long", TurnID: "turn", Role: "assistant", Timestamp: protocol.Number(1),
	}, Text: protocol.Text(text)}})
	if len(parts) < 2 {
		t.Fatalf("parts = %d, want a split", len(parts))
	}
	for index, part := range parts {
		if !part.TextOffset.Known {
			t.Fatal("part lost its offset")
		}
		start, end := part.TextOffset.Value, part.TextOffset.Value+toolpreview.UTF16Len(string(part.Text))
		if got, want := string(part.Text), textRangeUnits(text, start, end); got != want {
			t.Fatalf("part %d = %q, want %q", index, got, want)
		}
		if !part.TextContinues.Value && index != len(parts)-1 {
			t.Fatalf("part %d does not continue", index)
		}
	}
}
