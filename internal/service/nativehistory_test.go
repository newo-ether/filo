package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
)

func nativePageFixture(t *testing.T, large bool) readerFunc {
	t.Helper()
	return func(ctx context.Context, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
		request := func(_ context.Context, method string, raw any) (any, error) {
			params := raw.(map[string]any)
			switch method {
			case "thread/read":
				if params["includeTurns"] != false {
					t.Error("full thread read")
				}
				return map[string]any{"thread": map[string]any{"id": id, "status": map[string]any{"type": "notLoaded"}}}, nil
			case "thread/turns/list":
				return map[string]any{"data": []any{map[string]any{"id": "turn", "status": "completed"}}, "nextCursor": nil}, nil
			case "thread/items/list":
				start, end := 16, 32
				var next any = "older"
				if params["cursor"] != nil {
					start, end, next = 0, 16, nil
				}
				rows := []any{}
				for i := end - 1; i >= start; i-- {
					text := fmt.Sprint("body-", i)
					if large && i == 31 {
						text = strings.Repeat("a\n", 1200000)
					}
					rows = append(rows, map[string]any{"turnId": "turn", "item": map[string]any{"id": fmt.Sprint("item-", i), "type": "agentMessage", "text": text}})
				}
				return map[string]any{"data": rows, "nextCursor": next}, nil
			default:
				return nil, fmt.Errorf("unexpected method %s", method)
			}
		}
		return codex.ReadHistory(ctx, request, "home", id, cursor, activity, excluded)
	}
}

func TestNativeHistoryWorkerReplayAndBoundedTraversal(t *testing.T) {
	source := nativePageFixture(t, true)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Query().Get("cursor") == "" {
			t.Error("native source was not fixed")
		}
		page, err := ReadConversationPage(r.Context(), source, "thread", r.URL.Query().Get("cursor"), PageOptions{Activity: true, Layer: "worker"})
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		text, err := EncodeResponse(page)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(text))
	}))
	defer server.Close()
	worker := newUserWorkerFixture(t, server.URL, StaticUserWorkerToken(strings.Repeat("a", 64)), userWorkerUnusedHistory{}, DefaultUserWorkerTimeouts)
	read := workerHistoryReader(&worker)
	proxy := readerFunc(func(ctx context.Context, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
		return read(ctx, "home", id, cursor, activity, excluded)
	})
	cursor := ""
	seen := map[string]bool{}
	var all []protocol.Message
	pages := 0
	for {
		page, err := ReadConversationPage(context.Background(), proxy, "thread", cursor, PageOptions{Activity: true, IncludeMetadata: true})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := EncodeResponse(page)
		if err != nil || len(encoded) > MaxConversationPageBytes {
			t.Fatal("unbounded page", len(encoded), err)
		}
		if len(page.Messages) == 0 || !page.PageCursor.Known || len(page.PageCursor.Value) > maxCursorUnits {
			t.Fatal("missing/oversized replay cursor")
		}
		replay, err := ReadConversationPage(context.Background(), proxy, "thread", page.PageCursor.Value, PageOptions{Activity: true, IncludeMetadata: true})
		if err != nil || !reflect.DeepEqual(messageIDs(replay.Messages), messageIDs(page.Messages)) {
			t.Fatal("replay differs", err)
		}
		for _, message := range page.Messages {
			if seen[message.ID] {
				t.Fatal("duplicate message", message.ID)
			}
			seen[message.ID] = true
		}
		all = append(page.Messages, all...)
		pages++
		if page.NextCursor == nil {
			break
		}
		if *page.NextCursor == cursor || pages > 30 {
			t.Fatal("cursor did not advance")
		}
		cursor = *page.NextCursor
	}
	text := strings.Builder{}
	for _, message := range all {
		if message.ID == "item-31" || message.NativeID == "item-31" {
			text.WriteString(string(message.Text))
		}
	}
	if text.String() != strings.Repeat("a\n", 1200000) || pages < 3 || requests < 6 {
		t.Fatal("lost split text or pagination", pages, requests)
	}
	if _, err := proxy.Read(context.Background(), "other", cursor, true, nil); err == nil {
		t.Fatal("foreign worker cursor accepted")
	}
}

func TestNativeHistoryWorkerRejectsInvalidReplies(t *testing.T) {
	for _, value := range []any{map[string]any{}, map[string]any{"messages": []any{}, "runtime": map[string]any{"status": "idle"}}, map[string]any{"messages": nil}} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(value) }))
			defer server.Close()
			worker := newUserWorkerFixture(t, server.URL, StaticUserWorkerToken(strings.Repeat("a", 64)), userWorkerUnusedHistory{}, DefaultUserWorkerTimeouts)
			if _, err := workerHistoryReader(&worker)(context.Background(), "home", "thread", "", true, nil); err == nil {
				t.Fatal("invalid helper page became empty history")
			}
		})
	}
	if _, err := nativeHistoryReader(nil)(context.Background(), "home", "thread", "", false, nil); err == nil {
		t.Fatal("missing native host accepted")
	}
}

func TestNativeHistoryEmptyExcludedPagesRetainReplayAndProgress(t *testing.T) {
	page, err := ReadConversationPage(context.Background(), nativePageFixture(t, false), "thread", "", PageOptions{ExcludedTurns: []string{"turn"}})
	if err != nil || len(page.Messages) != 0 || page.NextCursor == nil || !page.PageCursor.Known {
		t.Fatalf("empty excluded page lost identity: %+v, %v", page, err)
	}
	last, err := ReadConversationPage(context.Background(), nativePageFixture(t, false), "thread", *page.NextCursor, PageOptions{ExcludedTurns: []string{"turn"}})
	if err != nil || len(last.Messages) != 0 || last.NextCursor != nil || !last.PageCursor.Known {
		t.Fatalf("empty final page: %+v, %v", last, err)
	}
}
