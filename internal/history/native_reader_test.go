package history

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/codex"
	"github.com/newo-ether/filo/internal/protocol"
)

func TestNativeHistoryRoutesOnlyUnavailableInitialFiles(t *testing.T) {
	f := nativeHistoryFixture(t, 0, 33)
	calls := 0
	failure := errors.New("native read failed")
	f.history.native = func(_ context.Context, home, id, cursor string, activity bool, excluded []string) (protocol.ConversationPage, error) {
		calls++
		if home != f.home || id != "task" || !activity {
			t.Fatal("routing lost identity/options")
		}
		return protocol.ConversationPage{}, failure
	}
	first := f.read(t, "")
	if calls != 0 || first.NextCursor == nil {
		t.Fatal("readable plain file used native")
	}
	if err := os.Rename(f.path, f.path+".zst"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.history.Read(context.Background(), "task", "", true, nil); !errors.Is(err, failure) || calls != 1 {
		t.Fatal(err, calls)
	}
	if _, err := f.history.Read(context.Background(), "task", *first.NextCursor, true, nil); err == nil || calls != 1 {
		t.Fatal("file continuation switched source", err, calls)
	}
	if err := os.Rename(f.path+".zst", f.path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.history.Read(context.Background(), "task", codex.HistoryStartCursor(f.home, "task"), true, nil); !errors.Is(err, failure) || calls != 2 {
		t.Fatal("native cursor switched to file", err, calls)
	}
	f.write(t, Catalog, func(db *sql.DB) {
		if _, err := db.Exec("UPDATE threads SET rollout_path=? WHERE id='task'", f.path+".zst"); err != nil {
			t.Fatal(err)
		}
	})
	f.history.native = nil
	if _, err := f.history.Read(context.Background(), "task", "", true, nil); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatal(err)
	}
	if _, err := f.history.Read(context.Background(), "task", *first.NextCursor, true, nil); err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatal(err)
	}
}
