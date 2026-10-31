package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/history"
	"github.com/newo-ether/filo/internal/protocol"
)

// nativeHistoryReader uses the helper's existing original runtime connection.
// No attach, resume, writer acquisition or process start is part of this read.
func nativeHistoryReader(host *codex.CodexHost) history.NativeReader {
	return func(ctx context.Context, home, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
		if host == nil || host.Rpc == nil {
			return protocol.ConversationPage{}, errors.New("Native history connection is unavailable")
		}
		if !filepath.IsAbs(host.CodexHome) || !strings.EqualFold(filepath.Clean(home), filepath.Clean(host.CodexHome)) {
			return protocol.ConversationPage{}, errors.New("Native history store identity mismatch")
		}
		return codex.ReadHistory(ctx, func(ctx context.Context, method string, params any) (any, error) {
			switch method {
			case "thread/read", "thread/turns/list", "thread/items/list":
				return host.Rpc.RequestContext(ctx, method, params, 20*time.Second)
			default:
				return nil, errors.New("Native history operation is not read-only")
			}
		}, home, id, cursor, activity, excluded)
	}
}

const workerHistoryPrefix = "filo.native.rpc.worker."

type workerHistoryCursor struct {
	ID     string
	Home   string
	Cursor string
}

func wrapWorkerHistoryCursor(home, id, cursor string) (string, error) {
	body, _ := json.Marshal(workerHistoryCursor{id, home, cursor})
	token := workerHistoryPrefix + base64.RawURLEncoding.EncodeToString(body)
	if cursor == "" || len(token) > 5000 {
		return "", errors.New("Native worker history cursor exceeds its bound")
	}
	return token, nil
}

// workerHistoryReader forwards unavailable direct history to the selected user's
// verified helper. The helper owns native store checks and public page limits.
func workerHistoryReader(worker **UserWorker) history.NativeReader {
	return func(ctx context.Context, home, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
		page := protocol.ConversationPage{}
		query := url.Values{"includeActivity": {"false"}, "cursor": {codex.HistoryStartCursor(home, id)}}
		if activity {
			query.Set("includeActivity", "true")
		}
		if cursor != "" {
			var token workerHistoryCursor
			body, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, workerHistoryPrefix))
			if len(cursor) > 5000 || !strings.HasPrefix(cursor, workerHistoryPrefix) || err != nil || json.Unmarshal(body, &token) != nil || token.ID != id || token.Home != home || token.Cursor == "" {
				return page, errors.New("Invalid native worker history cursor")
			}
			query.Set("cursor", token.Cursor)
		}
		if worker == nil || *worker == nil {
			return page, errors.New("Native history helper is unavailable")
		}
		body, err := (*worker).call(ctx, "/v1/sessions/"+url.PathEscape(id)+"?"+query.Encode(), nil)
		if err != nil {
			return page, err
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return page, err
		}
		if page.Messages == nil || page.Runtime == nil || page.Runtime.Status == "" || !page.PageCursor.Known || page.PageCursor.Value == "" || (page.NextCursor != nil && *page.NextCursor == "") {
			return protocol.ConversationPage{}, errors.New("Invalid native helper history page")
		}
		messages := page.Messages[:0]
		for _, message := range page.Messages {
			if !slices.Contains(excluded, message.TurnID) {
				messages = append(messages, message)
			}
		}
		page.Messages = messages
		if page.NextCursor != nil {
			token, err := wrapWorkerHistoryCursor(home, id, *page.NextCursor)
			if err != nil {
				return protocol.ConversationPage{}, err
			}
			page.NextCursor = &token
		}
		token, err := wrapWorkerHistoryCursor(home, id, page.PageCursor.Value)
		if err != nil {
			return protocol.ConversationPage{}, err
		}
		page.PageCursor = protocol.Known(token)
		return page, ctx.Err()
	}
}
