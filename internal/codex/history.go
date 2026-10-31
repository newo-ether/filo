package codex

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

const historyRPCPrefix = "filo.native.rpc."

// HistoryRequest is restricted to the three read-only history operations below.
type HistoryRequest func(context.Context, string, any) (any, error)

type historyRPCursor struct {
	ID    string `json:"id"`
	Store string `json:"store"`
	After string `json:"after"`
}

// HistoryStartCursor selects native history even when a plain rollout later appears.
func HistoryStartCursor(home, id string) string {
	return encodeHistoryCursor(id, fmt.Sprintf("%x", sha256.Sum256([]byte(home))), "")
}

func encodeHistoryCursor(id, store, after string) string {
	body, _ := json.Marshal(historyRPCursor{id, store, after})
	return historyRPCPrefix + base64.RawURLEncoding.EncodeToString(body)
}

// ReadHistory projects one native item page, never a complete thread snapshot.
// Native cursors retain their own ordering and are bound to this store and thread.
func ReadHistory(ctx context.Context, request HistoryRequest, home, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
	page := protocol.ConversationPage{Messages: []protocol.Message{}, Queued: []protocol.QueuedInput{}}
	store := fmt.Sprintf("%x", sha256.Sum256([]byte(home)))
	after := ""
	if cursor != "" {
		var token historyRPCursor
		body, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, historyRPCPrefix))
		if len(cursor) > 2048 || !strings.HasPrefix(cursor, historyRPCPrefix) || err != nil || json.Unmarshal(body, &token) != nil || token.ID != id || token.Store != store {
			return page, errors.New("Invalid native RPC history cursor")
		}
		after = token.After
	}
	value, err := request(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false})
	if err != nil {
		return page, err
	}
	fields, _ := nativejson.Fields(value)
	thread, ok := nativejson.Fields(fields["thread"])
	if !ok || nativejson.Text(thread["id"]) != id {
		return page, errors.New("Native history identity mismatch")
	}
	value, err = request(ctx, "thread/turns/list", map[string]any{"threadId": id, "limit": 1, "sortDirection": "desc", "itemsView": "summary"})
	if err != nil {
		return page, err
	}
	turns, _, err := historyData(value, 1)
	if err != nil {
		return page, err
	}
	var latest map[string]any
	if len(turns) > 0 {
		latest, ok = nativejson.Fields(turns[0])
		if !ok || nativejson.Text(latest["id"]) == "" || nativejson.Text(latest["status"]) == "" {
			return page, errors.New("Invalid native history turn")
		}
	}
	page.Runtime = historyRuntime(thread, latest)
	params := map[string]any{"threadId": id, "limit": 16, "sortDirection": "desc"}
	if after != "" {
		params["cursor"] = after
	}
	value, err = request(ctx, "thread/items/list", params)
	if err != nil {
		return page, err
	}
	rows, next, err := historyData(value, 16)
	if err != nil {
		return page, err
	}
	if next != "" && (next == after || len(rows) == 0) {
		return page, errors.New("Native history pagination did not advance")
	}
	page.InputReceipts, err = historyInputReceipts(ctx, request, id, after, rows, next)
	if err != nil {
		return page, err
	}
	seen := map[string]bool{}
	for i := len(rows) - 1; i >= 0; i-- {
		row, _ := nativejson.Fields(rows[i])
		turnID := nativejson.Text(row["turnId"])
		item, ok := nativejson.Fields(row["item"])
		itemID := nativejson.Text(item["id"])
		if !ok || turnID == "" || itemID == "" || seen[itemID] {
			return page, errors.New("Invalid native history item identity")
		}
		seen[itemID] = true
		if slices.Contains(excluded, turnID) {
			continue
		}
		var started *float64
		if ms, ok := row["startedAtMs"].(float64); ok {
			seconds := ms / 1000
			started = &seconds
		}
		page.Messages = append(page.Messages, ProjectMessages([]NativeTurn{{ID: turnID, Items: []map[string]any{item}, StartedAt: started}}, activity)...)
	}
	if after == "" && latest != nil && !slices.Contains(excluded, nativejson.Text(latest["id"])) {
		page.Messages = append(page.Messages, ProjectMessages([]NativeTurn{{ID: nativejson.Text(latest["id"]), Status: latest["status"], Error: latest["error"]}}, activity)...)
	}
	page.PageCursor = protocol.Known(encodeHistoryCursor(id, store, after))
	if next != "" {
		token := encodeHistoryCursor(id, store, next)
		if len(token) > 2048 {
			return page, errors.New("Native history cursor exceeds its bound")
		}
		page.NextCursor = &token
	}
	return page, ctx.Err()
}

// historyInputReceipts examines at most the latest 128 native items, independently
// of the visible page and excluded turns. It retains identities, never item bodies.
func historyInputReceipts(ctx context.Context, request HistoryRequest, id, after string, rows []any, next string) ([]protocol.InputReceipt, error) {
	var receipts []protocol.InputReceipt
	if after != "" {
		rows, after = nil, ""
	}
	seenItems, seenCursors := map[string]bool{}, map[string]bool{}
	for count := 0; count < 128; {
		if rows == nil || count != 0 {
			params := map[string]any{"threadId": id, "limit": 16, "sortDirection": "desc"}
			if count != 0 {
				params["cursor"] = after
			}
			value, err := request(ctx, "thread/items/list", params)
			if err != nil {
				return nil, err
			}
			rows, next, err = historyData(value, 16)
			if err != nil {
				return nil, err
			}
		}
		if next != "" && (next == after || seenCursors[next] || len(next) > 2048 || len(rows) == 0) {
			return nil, errors.New("Native history receipt pagination did not advance")
		}
		for _, raw := range rows {
			row, _ := nativejson.Fields(raw)
			item, ok := nativejson.Fields(row["item"])
			turnID, itemID := nativejson.Text(row["turnId"]), nativejson.Text(item["id"])
			if !ok || turnID == "" || itemID == "" || seenItems[itemID] {
				return nil, errors.New("Invalid native history receipt item identity")
			}
			seenItems[itemID] = true
			if nativejson.Text(item["type"]) == "userMessage" {
				receipts = append(receipts, protocol.InputReceipt{ClientID: nativejson.Text(item["clientId"]), TurnID: turnID, MessageID: itemID})
			}
			count++
			if count == 128 {
				break
			}
		}
		if next == "" || count == 128 {
			break
		}
		seenCursors[next] = true
		after = next
	}
	slices.Reverse(receipts)
	return protocol.MergeInputReceipts(receipts), ctx.Err()
}

func historyData(value any, limit int) ([]any, string, error) {
	fields, ok := nativejson.Fields(value)
	data, valid := fields["data"].([]any)
	next, hasNext := fields["nextCursor"]
	text, isText := nativejson.AsText(next)
	if !ok || !valid || data == nil || len(data) > limit || !hasNext || (next != nil && (!isText || text == "")) {
		return nil, "", errors.New("Invalid native history page")
	}
	return data, text, nil
}

func historyRuntime(thread, turn map[string]any) *protocol.Runtime {
	status, _ := nativejson.Fields(thread["status"])
	runtime := &protocol.Runtime{Status: nativejson.Text(status["type"])}
	if runtime.Status == "" {
		runtime.Status = "notLoaded"
	}
	title := nativejson.Text(thread["name"])
	if title == "" {
		title = nativejson.Text(thread["preview"])
	}
	text := protocol.Text(title)
	runtime.Title = &text
	if model, ok := nativejson.AsText(thread["model"]); ok {
		runtime.Model = &model
	}
	runtime.Effort = optionalNullableText(thread, "reasoningEffort")
	runtime.ServiceTier = optionalNullableText(thread, "serviceTier")
	if runtime.ServiceTier.Known {
		runtime.ServiceTierKnown = protocol.Known(true)
	}
	if turn != nil {
		id := nativejson.Text(turn["id"])
		runtime.CompletedTurnID = protocol.Known[*string](nil)
		switch nativejson.Text(turn["status"]) {
		case "completed", "failed", "interrupted":
			runtime.CompletedTurnID = protocol.Known(&id)
			if runtime.Status == "notLoaded" {
				runtime.Status = "idle"
			}
		case "inProgress":
			if runtime.Status == "active" {
				runtime.ActiveTurnID = &id
			}
		}
	}
	return runtime
}
