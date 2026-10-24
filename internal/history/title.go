package history

import (
	"context"

	"github.com/newo-ether/filo/internal/protocol"
)

// Title reads fresh native metadata without invalidating the bounded history cache.
func (history *NativeHistory) Title(ctx context.Context, id string) (*protocol.Text, error) {
	home, err := history.home(ctx)
	if err != nil {
		return nil, err
	}
	db, err := OpenDatabase(ctx, home, Catalog)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var title string
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(NULLIF(name,''),title,'') FROM threads WHERE id=?", id).Scan(&title); err != nil {
		return nil, err
	}
	value := protocol.Text(title)
	return &value, nil
}
