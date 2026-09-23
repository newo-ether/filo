package history

import (
	"context"
	"errors"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

// NativeHistory resolves one selected original account per read. It cannot
// acquire an owner, launch a native process or mutate a native database.
type NativeHistory struct {
	home func(context.Context) (string, error)
}

func NewNativeHistory(home func(context.Context) (string, error)) *NativeHistory {
	return &NativeHistory{home: home}
}

func (history *NativeHistory) List(ctx context.Context, cursor string) (protocol.SessionPage, error) {
	page := protocol.SessionPage{Sessions: make([]protocol.Session, 0)}
	fields, err := decodeCursor("list", cursor)
	if err != nil {
		return page, err
	}
	query := "SELECT id,COALESCE(name,title),cwd,updated_at FROM threads WHERE archived=0 "
	var args []any
	if fields != nil {
		at, atOK := safeInteger(fields["at"])
		id, idOK := nativejson.AsText(fields["id"])
		if !atOK || !idOK {
			return page, errors.New("Invalid native list cursor")
		}
		query += "AND (updated_at < ? OR (updated_at = ? AND id < ?)) "
		args = []any{at, at, id}
	}
	home, err := history.home(ctx)
	if err != nil {
		return page, err
	}
	db, err := OpenDatabase(ctx, home, Catalog)
	if err != nil {
		return page, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, query+"ORDER BY updated_at DESC,id DESC LIMIT 31", args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var lastAt int64
	for rows.Next() {
		var session protocol.Session
		var title string
		var at int64
		if err := rows.Scan(&session.ID, &title, &session.Cwd, &at); err != nil {
			return page, err
		}
		if len(page.Sessions) == 30 {
			last := page.Sessions[29]
			next := encodeCursor("list", struct {
				At int64  `json:"at"`
				ID string `json:"id"`
			}{lastAt, last.ID})
			page.NextCursor = &next
			break
		}
		session.Title = protocol.Text(title)
		session.UpdatedAt = float64(at)
		session.Status = protocol.Known[*string](nil)
		page.Sessions = append(page.Sessions, session)
		lastAt = at
	}
	return page, rows.Err()
}
