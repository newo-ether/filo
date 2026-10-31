package codex

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func historyResponse(rows []any, next any) map[string]any {
	return map[string]any{"data": rows, "nextCursor": next}
}

func historyItem(id, turn string) any {
	return map[string]any{"turnId": turn, "item": map[string]any{"id": id, "type": "agentMessage", "text": id}}
}

func historyRequestFixture(t *testing.T, items func(map[string]any) (any, error)) HistoryRequest {
	t.Helper()
	return func(_ context.Context, method string, value any) (any, error) {
		params := value.(map[string]any)
		if params["threadId"] != "thread" {
			t.Fatalf("wrong thread: %v", params)
		}
		switch method {
		case "thread/read":
			if params["includeTurns"] != false {
				t.Fatal("full history requested")
			}
			return map[string]any{"thread": map[string]any{"id": "thread", "status": map[string]any{"type": "notLoaded"}}}, nil
		case "thread/turns/list":
			if params["limit"] != 1 || params["itemsView"] != "summary" || params["sortDirection"] != "desc" {
				t.Fatalf("unbounded turn read: %v", params)
			}
			return historyResponse([]any{map[string]any{"id": "turn", "status": "completed"}}, nil), nil
		case "thread/items/list":
			if params["limit"] != 16 || params["sortDirection"] != "desc" {
				t.Fatalf("unbounded item read: %v", params)
			}
			return items(params)
		default:
			t.Fatalf("unexpected method %s", method)
			return nil, errors.New("unexpected method")
		}
	}
}

func TestNativeHistoryPaginationAndSourceBinding(t *testing.T) {
	request := historyRequestFixture(t, func(params map[string]any) (any, error) {
		if params["cursor"] == nil {
			return historyResponse([]any{historyItem("new", "turn"), historyItem("middle", "turn")}, "older"), nil
		}
		if params["cursor"] != "older" {
			t.Fatal(params)
		}
		return historyResponse([]any{historyItem("old", "old-turn")}, nil), nil
	})
	ctx := context.Background()
	first, err := ReadHistory(ctx, request, "home", "thread", "", true, nil)
	if err != nil || first.NextCursor == nil || !first.PageCursor.Known || first.Runtime.Status != "idle" || *first.Runtime.CompletedTurnID.Value != "turn" {
		t.Fatalf("first: %+v, %v", first, err)
	}
	if len(first.Messages) != 2 || first.Messages[0].ID != "middle" || first.Messages[1].ID != "new" {
		t.Fatal(first.Messages)
	}
	replay, err := ReadHistory(ctx, request, "home", "thread", first.PageCursor.Value, true, nil)
	if err != nil || !reflect.DeepEqual(replay.Messages, first.Messages) {
		t.Fatalf("replay: %v", err)
	}
	last, err := ReadHistory(ctx, request, "home", "thread", *first.NextCursor, true, nil)
	if err != nil || last.NextCursor != nil || len(last.Messages) != 1 || last.Messages[0].ID != "old" {
		t.Fatalf("last: %+v, %v", last, err)
	}
	excluded, err := ReadHistory(ctx, request, "home", "thread", "", true, []string{"turn"})
	if err != nil || len(excluded.Messages) != 0 || excluded.NextCursor == nil {
		t.Fatalf("excluded page lost progress: %+v, %v", excluded, err)
	}
	for _, token := range []string{HistoryStartCursor("other-home", "thread"), HistoryStartCursor("home", "other-thread"), "filo.native.rpc.invalid", strings.Repeat("x", 2049)} {
		_, err := ReadHistory(ctx, request, "home", "thread", token, true, nil)
		if err == nil {
			t.Fatal("foreign/malformed cursor accepted")
		}
	}
}

func TestNativeHistoryRejectsMalformedPagesAndPropagatesFailure(t *testing.T) {
	failure := errors.New("native unavailable")
	for _, response := range []any{
		map[string]any{}, historyResponse(nil, nil), historyResponse([]any{}, "stuck"),
		historyResponse([]any{historyItem("same", "turn"), historyItem("same", "turn")}, nil),
		historyResponse([]any{map[string]any{"item": map[string]any{"id": "missing-turn"}}}, nil),
	} {
		request := historyRequestFixture(t, func(map[string]any) (any, error) { return response, nil })
		if _, err := ReadHistory(context.Background(), request, "home", "thread", "", true, nil); err == nil {
			t.Fatalf("accepted malformed response: %v", response)
		}
	}
	request := historyRequestFixture(t, func(map[string]any) (any, error) { return nil, failure })
	if _, err := ReadHistory(context.Background(), request, "home", "thread", "", true, nil); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	request = historyRequestFixture(t, func(map[string]any) (any, error) {
		return historyResponse([]any{historyItem("x", "turn")}, "same"), nil
	})
	first, err := ReadHistory(context.Background(), request, "home", "thread", "", true, nil)
	if err == nil || !strings.Contains(err.Error(), "did not advance") {
		t.Fatal("receipt traversal accepted repeated native continuation", err, first)
	}
}

func TestNativeHistoryReceiptsAreIndependentOfVisiblePage(t *testing.T) {
	calls := 0
	request := historyRequestFixture(t, func(params map[string]any) (any, error) {
		calls++
		offset := 0
		if params["cursor"] != nil {
			if _, err := fmt.Sscanf(params["cursor"].(string), "offset-%d", &offset); err != nil {
				t.Fatal(err)
			}
		}
		rows := []any{}
		for i := offset; i < offset+16; i++ {
			item := map[string]any{"id": fmt.Sprint("item-", i), "type": "agentMessage", "text": "body"}
			if i == 31 || i == 127 || i == 128 {
				item["type"], item["clientId"] = "userMessage", fmt.Sprint("client-", i)
			}
			rows = append(rows, map[string]any{"turnId": "turn", "item": item})
		}
		return historyResponse(rows, fmt.Sprint("offset-", offset+16)), nil
	})
	for _, after := range []string{"", "offset-144"} {
		calls = 0
		cursor := encodeHistoryCursor("thread", fmt.Sprintf("%x", sha256.Sum256([]byte("home"))), after)
		page, err := ReadHistory(context.Background(), request, "home", "thread", cursor, true, []string{"turn"})
		if err != nil || len(page.Messages) != 0 || len(page.InputReceipts) != 2 {
			t.Fatalf("receipt window: %+v, %v", page, err)
		}
		if page.InputReceipts[0].ClientID != "client-127" || page.InputReceipts[1].ClientID != "client-31" {
			t.Fatal(page.InputReceipts)
		}
		want := 8
		if after != "" {
			want++
		}
		if calls != want {
			t.Fatalf("item calls = %d, want bounded %d", calls, want)
		}
	}
}

func TestNativeHistoryRuntimeRequiresTerminalEvidence(t *testing.T) {
	for _, status := range []string{"", "inProgress", "completed", "failed", "interrupted"} {
		thread := map[string]any{"status": map[string]any{"type": "notLoaded"}}
		var turn map[string]any
		if status != "" {
			turn = map[string]any{"id": "turn", "status": status}
		}
		runtime := historyRuntime(thread, turn)
		terminal := status == "completed" || status == "failed" || status == "interrupted"
		if (runtime.Status == "idle") != terminal || runtime.ActiveTurnID != nil || (runtime.CompletedTurnID.Known && runtime.CompletedTurnID.Value != nil) != terminal {
			t.Fatalf("%s: %+v", status, runtime)
		}
	}
}
