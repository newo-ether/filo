package history

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

type indexRow struct {
	id, turn       string
	ordinal, bytes int64
	created        float64
}

func readIndexed(ctx context.Context, tx *sql.Tx, id string, after *itemCursor, activity bool, excluded []string) (*MessagePage, error) {
	query := "SELECT item_id,turn_id,rollout_ordinal,created_at_ms,length(CAST(item_json AS BLOB)) FROM thread_items WHERE thread_id=? "
	args := []any{id}
	if after != nil {
		comparison := "<"
		if after.Inclusive {
			comparison = "<="
		}
		query += "AND (rollout_ordinal < ? OR (rollout_ordinal = ? AND item_id " + comparison + " ?)) "
		args = append(args, after.Ordinal, after.Ordinal, after.Item)
	}
	rows, err := tx.QueryContext(ctx, query+"ORDER BY rollout_ordinal DESC,item_id DESC LIMIT 17", args...)
	if err != nil {
		return nil, err
	}
	var found []indexRow
	for rows.Next() {
		var row indexRow
		if err := rows.Scan(&row.id, &row.turn, &row.ordinal, &row.created, &row.bytes); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	shown := found[:min(16, len(found))]
	page := &MessagePage{Messages: make([]protocol.Message, 0)}
	for i := len(shown) - 1; i >= 0; i-- {
		row := shown[i]
		if slices.Contains(excluded, row.turn) {
			continue
		}
		reader, err := newBlobReader(ctx, tx, row.bytes, "SELECT substr(CAST(item_json AS BLOB),?,65536) FROM thread_items WHERE thread_id=? AND turn_id=? AND item_id=?", id, row.turn, row.id)
		if err != nil {
			return nil, err
		}
		value, _, err := nativejson.Read(io.MultiReader(strings.NewReader(`{"items":[`), reader, strings.NewReader(`]}`)), false)
		if err != nil {
			return nil, err
		}
		fields, _ := nativejson.Fields(value)
		items, _ := fields["items"].([]any)
		if len(items) != 1 {
			return nil, errors.New("Native indexed item identity mismatch")
		}
		item, _ := nativejson.Fields(items[0])
		if nativejson.Text(item["id"]) != row.id {
			return nil, errors.New("Native indexed item identity mismatch")
		}
		started := row.created / 1000
		page.Messages = append(page.Messages, codex.ProjectMessages([]codex.NativeTurn{{ID: row.turn, Items: []map[string]any{item}, StartedAt: &started}}, activity)...)
	}
	if len(found) > 16 {
		last := shown[len(shown)-1]
		next := encodeCursor("items", itemCursor{id, last.ordinal, last.id, false})
		page.NextCursor = &next
	}
	return page, nil
}

// Each SQLite read returns at most one parser-sized chunk inside the same read
// snapshot. Excluded turns never instantiate this reader or fetch item bodies.
type blobReader struct {
	ctx          context.Context
	tx           *sql.Tx
	query        string
	args         []any
	size, offset int64
	buffer       []byte
}

func newBlobReader(ctx context.Context, tx *sql.Tx, size int64, query string, args ...any) (*blobReader, error) {
	if size < 0 || size > maxNativeRecordBytes {
		return nil, errors.New("Native indexed item exceeds its read bound")
	}
	return &blobReader{ctx: ctx, tx: tx, size: size, query: query, args: args}, nil
}

func (reader *blobReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if len(reader.buffer) == 0 {
		if reader.offset >= reader.size {
			return 0, io.EOF
		}
		args := append([]any{reader.offset + 1}, reader.args...)
		if err := reader.tx.QueryRowContext(reader.ctx, reader.query, args...).Scan(&reader.buffer); err != nil {
			return 0, err
		}
		if int64(len(reader.buffer)) != min(int64(scanBytes), reader.size-reader.offset) {
			return 0, errors.New("Native indexed item changed during read")
		}
		reader.offset += int64(len(reader.buffer))
	}
	n := copy(buffer, reader.buffer)
	reader.buffer = reader.buffer[n:]
	return n, nil
}

func readTerminalError(ctx context.Context, tx *sql.Tx, id string, activity bool, excluded []string) ([]protocol.Message, error) {
	var turn, status string
	var bytes sql.NullInt64
	var started *float64
	err := tx.QueryRowContext(ctx, "SELECT turn_id,status,length(CAST(error_json AS BLOB)),started_at FROM thread_turns WHERE thread_id=? ORDER BY rollout_ordinal DESC LIMIT 1", id).Scan(&turn, &status, &bytes, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if status != "failed" || slices.Contains(excluded, turn) {
		return nil, nil
	}
	var nativeError any
	if bytes.Valid && bytes.Int64 > 0 {
		reader, err := newBlobReader(ctx, tx, bytes.Int64, "SELECT substr(CAST(error_json AS BLOB),?,65536) FROM thread_turns WHERE thread_id=? AND turn_id=?", id, turn)
		if err != nil {
			return nil, err
		}
		nativeError, _, err = nativejson.Read(reader, false)
		if err != nil {
			return nil, err
		}
	}
	return codex.ProjectMessages([]codex.NativeTurn{{ID: turn, Status: status, StartedAt: started, Error: nativeError}}, activity), nil
}
