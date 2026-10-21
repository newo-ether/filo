package codexcli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/newo-ether/filo/internal/history"
)

// These are generated native files, never an installed user's store.
func TestPersistedNativeHistoryWithoutDaemonPagesAndRemainsUnmodified(t *testing.T) {
	home := t.TempDir()
	rollout := filepath.Join(home, "rollout.jsonl")
	if err := os.WriteFile(rollout, []byte(fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":%q}}\n", firstID)), 0600); err != nil {
		t.Fatal(err)
	}
	catalog := fixtureDatabase(t, filepath.Join(home, "state_5.sqlite"), `CREATE TABLE threads(id TEXT PRIMARY KEY,name TEXT,title TEXT,cwd TEXT,updated_at INTEGER,archived INTEGER,rollout_path TEXT,model TEXT,reasoning_effort TEXT)`)
	if _, err := catalog.Exec("INSERT INTO threads VALUES(?,?,?,?,?,?,?,?,?)", firstID, "Saved CLI thread", "", home, 1, 0, rollout, "native-model", "high"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	indexed := fixtureDatabase(t, filepath.Join(home, "thread_history_1.sqlite"), `CREATE TABLE thread_items(thread_id TEXT,turn_id TEXT,item_id TEXT,rollout_ordinal INTEGER,created_at_ms REAL,item_json TEXT);
CREATE TABLE thread_turns(thread_id TEXT,turn_id TEXT,status TEXT,error_json TEXT,started_at REAL,rollout_ordinal INTEGER)`)
	for i := range 33 {
		id := fmt.Sprintf("item-%02d", i)
		body, err := json.Marshal(map[string]any{"id": id, "type": "agentMessage", "text": fmt.Sprintf("Saved answer %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := indexed.Exec("INSERT INTO thread_items VALUES(?,?,?,?,?,?)", firstID, "native-turn", id, i, 1000+i, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := indexed.Close(); err != nil {
		t.Fatal(err)
	}
	before := fixtureStoreHashes(t, home)
	reader := history.NewNativeHistory(func(context.Context) (string, error) { return home, nil })
	a, err := New(Options{History: reader, Scope: func(context.Context) (string, error) { return "verified-fixture-store", nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	list, err := a.List(context.Background(), "")
	if err != nil || len(list.Sessions) != 1 || list.Sessions[0].Title != "Saved CLI thread" || list.Sessions[0].Access == nil {
		t.Fatalf("native catalog: %+v %v", list, err)
	}
	cursor := ""
	var all []string
	pages := 0
	for {
		page, err := a.Read(context.Background(), firstID, cursor, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Messages) > 16 || page.Access == nil || page.Runtime == nil || page.Runtime.Status != "notLoaded" {
			t.Fatalf("unbounded or invented native state: %+v", page)
		}
		var ids []string
		for _, message := range page.Messages {
			if message.TurnID != "native-turn" || message.Text == "" {
				t.Fatal("native identity/body was lost", message)
			}
			ids = append(ids, message.ID)
		}
		all = append(ids, all...)
		pages++
		if page.NextCursor == nil {
			break
		}
		if pages > 3 || cursor == *page.NextCursor {
			t.Fatal("native cursor did not finish or advance")
		}
		cursor = *page.NextCursor
	}
	var expected []string
	for i := range 33 {
		expected = append(expected, fmt.Sprintf("item-%02d", i))
	}
	if pages != 3 || !reflect.DeepEqual(all, expected) {
		t.Fatal("native messages were duplicated, omitted or reordered", pages, all)
	}
	if after := fixtureStoreHashes(t, home); !reflect.DeepEqual(before, after) {
		t.Fatal("observation modified native files or created sidecars", before, after)
	}
}

func fixtureDatabase(t *testing.T, path, schema string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func fixtureStoreHashes(t *testing.T, directory string) map[string][32]byte {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][32]byte)
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = sha256.Sum256(body)
	}
	return result
}
