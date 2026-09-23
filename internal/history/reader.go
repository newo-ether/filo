package history

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/newo-ether/filo/internal/protocol"
)

type NativeTaskNotIndexed struct{ TaskID string }

func (err *NativeTaskNotIndexed) Error() string { return "Native task was not found" }

func (history *NativeHistory) Read(ctx context.Context, id, cursor string, activity bool, excludedTurns []string) (protocol.ConversationPage, error) {
	result := protocol.ConversationPage{Messages: make([]protocol.Message, 0), Queued: make([]protocol.QueuedInput, 0)}
	home, err := history.home(ctx)
	if err != nil {
		return result, err
	}
	catalog, err := OpenDatabase(ctx, home, Catalog)
	if err != nil {
		return result, err
	}
	var threadID, path string
	var model, effort *string
	err = catalog.QueryRowContext(ctx, "SELECT id,rollout_path,model,reasoning_effort FROM threads WHERE id=?", id).Scan(&threadID, &path, &model, &effort)
	catalog.Close()
	if errors.Is(err, sql.ErrNoRows) {
		return result, &NativeTaskNotIndexed{TaskID: id}
	}
	if err != nil {
		return result, err
	}
	db, err := OpenDatabase(ctx, home, History)
	if err != nil {
		return result, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	raw := IsRolloutCursor(cursor)
	var after *itemCursor
	if !raw && cursor != IndexedHistoryCursor {
		after, err = decodeItems(id, cursor)
		if err != nil {
			return result, err
		}
	}
	var newestID string
	var newestOrdinal int64
	err = tx.QueryRowContext(ctx, "SELECT item_id,rollout_ordinal FROM thread_items WHERE thread_id=? ORDER BY rollout_ordinal DESC,item_id DESC LIMIT 1", id).Scan(&newestID, &newestOrdinal)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	boundary := IndexedHistoryCursor
	if err == nil {
		boundary = encodeCursor("items", itemCursor{id, newestOrdinal, newestID, true})
	}
	var tail *MessagePage
	if cursor == "" || raw {
		tail, err = ReadRolloutPage(ctx, path, id, cursor, newestID, activity, excludedTurns, boundary)
		if err != nil {
			return result, err
		}
	}
	reachesIndex := tail != nil && tail.NextCursor != nil && (*tail.NextCursor == IndexedHistoryCursor || strings.HasPrefix(*tail.NextCursor, nativePrefix+"items."))
	if tail != nil && (len(tail.Messages) > 0 || !reachesIndex) {
		result.Messages, result.NextCursor = tail.Messages, tail.NextCursor
	} else {
		if tail != nil && tail.NextCursor != nil && strings.HasPrefix(*tail.NextCursor, nativePrefix+"items.") {
			after, err = decodeItems(id, *tail.NextCursor)
			if err != nil {
				return result, err
			}
		}
		page, err := readIndexed(ctx, tx, id, after, activity, excludedTurns)
		if err != nil {
			return result, err
		}
		result.Messages, result.NextCursor = page.Messages, page.NextCursor
		if cursor == "" {
			terminal, err := readTerminalError(ctx, tx, id, activity, excludedTurns)
			if err != nil {
				return result, err
			}
			result.Messages = append(result.Messages, terminal...)
		}
	}
	result.Runtime = &protocol.Runtime{Status: "notLoaded", Model: model, Effort: protocol.Known(effort)}
	return result, nil
}
