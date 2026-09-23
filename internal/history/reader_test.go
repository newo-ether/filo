package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/newo-ether/filo/internal/protocol"
)

type nativeFixture struct {
	home, path string
	history    *NativeHistory
	event      func(int, any) string
	homeReads  int
}

func nativeHistoryFixture(t *testing.T, indexed, appended int) *nativeFixture {
	t.Helper()
	path, event := rolloutFixture(t, indexed+appended)
	home := filepath.Dir(path)
	databaseFixture(t, filepath.Join(home, "state_5.sqlite"), catalogSchema, "")
	databaseFixture(t, filepath.Join(home, "thread_history_1.sqlite"), historySchema, "")
	f := &nativeFixture{home: home, path: path, event: event}
	f.history = NewNativeHistory(func(context.Context) (string, error) { f.homeReads++; return home, nil })
	f.write(t, Catalog, func(db *sql.DB) {
		if _, err := db.Exec("INSERT INTO threads VALUES(?,?,?,?,?,?,?,?,?)", "task", "Task", "old", home, 1, 0, path, "native-model", "high"); err != nil {
			t.Fatal(err)
		}
	})
	f.write(t, History, func(db *sql.DB) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		for i := 0; i < indexed; i++ {
			body, _ := json.Marshal(map[string]any{"id": fmt.Sprint("item-", i), "type": "agentMessage", "text": fmt.Sprint("indexed-", i)})
			if _, err := tx.Exec("INSERT INTO thread_items VALUES(?,?,?,?,?,?)", "task", "old-turn", fmt.Sprint("item-", i), i, i+1000, string(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	})
	return f
}

func (f *nativeFixture) write(t *testing.T, kind DatabaseKind, write func(*sql.DB)) {
	t.Helper()
	name := "thread_history_1.sqlite"
	if kind == Catalog {
		name = "state_5.sqlite"
	}
	db, err := sql.Open("sqlite", filepath.Join(f.home, name))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	write(db)
}

func (f *nativeFixture) read(t *testing.T, cursor string, excluded ...string) protocol.ConversationPage {
	t.Helper()
	before := f.homeReads
	page, err := f.history.Read(context.Background(), "task", cursor, true, excluded)
	if err != nil {
		t.Fatal(err)
	}
	if f.homeReads != before+1 {
		t.Fatal("A read changed account between databases")
	}
	return page
}

func TestHistoryLatestPagesKeepCapturedIndexBoundaryDuringNativeCatchup(t *testing.T) {
	f := nativeHistoryFixture(t, 1, 33)
	before := fileHash(t, f.path)
	first := f.read(t, "")
	if !reflect.DeepEqual(messageIDs(first.Messages), expectedIDs(18, 34)) {
		t.Fatal(messageIDs(first.Messages))
	}
	if first.Runtime.Status != "notLoaded" || first.Runtime.ActiveTurnID != nil || *first.Runtime.Model != "native-model" || *first.Runtime.Effort.Value != "high" {
		t.Fatal(first.Runtime)
	}
	second := f.read(t, *first.NextCursor)
	if !reflect.DeepEqual(messageIDs(second.Messages), expectedIDs(2, 18)) {
		t.Fatal(messageIDs(second.Messages))
	}
	if fileHash(t, f.path) != before {
		t.Fatal("History read modified native records")
	}
	appendRollout(t, f.path, f.event(34, nil)+`{"type":"event_msg"`)
	if !reflect.DeepEqual(f.read(t, *first.NextCursor).Messages, second.Messages) {
		t.Fatal("Append changed an existing older page")
	}
	latest := f.read(t, "")
	if latest.Messages[len(latest.Messages)-1].ID != "item-34" {
		t.Fatal(messageIDs(latest.Messages))
	}
	third := f.read(t, *second.NextCursor)
	f.write(t, History, func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO thread_items VALUES('task','new-turn','item-34',34,1034,'{"id":"item-34","type":"agentMessage","text":"latest-34"}')`); err != nil {
			t.Fatal(err)
		}
	})
	old := f.read(t, *third.NextCursor)
	all := append(append(append(messageIDs(old.Messages), messageIDs(third.Messages)...), messageIDs(second.Messages)...), messageIDs(first.Messages)...)
	if !reflect.DeepEqual(all, expectedIDs(0, 34)) {
		t.Fatal("Index catchup duplicated or omitted native messages", all)
	}
}

func TestIndexedHistoryReadsBoundedChunksAndPreservesAllMessageOrder(t *testing.T) {
	f := nativeHistoryFixture(t, 65, 0)
	f.write(t, History, func(db *sql.DB) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		for i := 0; i < 65; i++ {
			body, _ := json.Marshal(map[string]any{"id": fmt.Sprint("item-", i), "type": "commandExecution", "command": "build", "aggregatedOutput": strings.Repeat("x", 1024*1024), "status": "completed"})
			if _, err := tx.Exec("UPDATE thread_items SET item_json=? WHERE item_id=?", string(body), fmt.Sprint("item-", i)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	})
	path := filepath.Join(f.home, "thread_history_1.sqlite")
	before := fileHash(t, path)
	var cursor string
	var ids []string
	seen := make(map[string]bool)
	pages := 0
	for {
		page := f.read(t, cursor)
		if len(page.Messages) > 16 {
			t.Fatal("Unbounded item page")
		}
		for _, message := range page.Messages {
			if message.Activity == nil || len(message.Activity.Result.Value) > 8300 {
				t.Fatal("Unbounded preview")
			}
		}
		wire, err := json.Marshal(page)
		if err != nil || len(wire) > 256*1024 {
			t.Fatal("Unbounded wire page", len(wire), err)
		}
		ids = append(messageIDs(page.Messages), ids...)
		pages++
		if pages > 5 {
			t.Fatal("Pagination did not finish")
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			t.Fatal("Cursor failed to advance")
		}
		seen[cursor] = true
	}
	if pages != 5 || !reflect.DeepEqual(ids, expectedIDs(0, 65)) {
		t.Fatal(pages, ids)
	}
	if fileHash(t, path) != before {
		t.Fatal("Native index was modified")
	}
}

func TestIndexedHistorySkipsCoveredBodiesAndRejectsForeignIdentities(t *testing.T) {
	f := nativeHistoryFixture(t, 17, 0)
	f.write(t, History, func(db *sql.DB) {
		if _, err := db.Exec("UPDATE thread_items SET item_json='invalid native JSON'"); err != nil {
			t.Fatal(err)
		}
	})
	page := f.read(t, "", "old-turn")
	if len(page.Messages) != 0 || page.NextCursor == nil || f.read(t, *page.NextCursor, "old-turn").NextCursor != nil {
		t.Fatal("Excluded turns lost their boundary")
	}
	if _, err := f.history.Read(context.Background(), "task", "", true, nil); err == nil {
		t.Fatal("Malformed uncovered body accepted")
	}
	if _, err := f.history.Read(context.Background(), "task", encodeCursor("items", itemCursor{"foreign", 2, "x", false}), true, nil); err == nil {
		t.Fatal("Foreign item cursor accepted")
	}
	f.write(t, History, func(db *sql.DB) {
		if _, err := db.Exec(`UPDATE thread_items SET item_json='{"id":"wrong","type":"agentMessage","text":"Wrong identity"}' WHERE item_id='item-0'`); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := f.history.Read(context.Background(), "task", *page.NextCursor, true, nil); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatal(err)
	}
	_, err := f.history.Read(context.Background(), "absent", "", true, nil)
	var missing *NativeTaskNotIndexed
	if !errors.As(err, &missing) || missing.TaskID != "absent" {
		t.Fatal(err)
	}
}

func TestHistoryPreservesFailedTurnErrorWithoutInventingLiveState(t *testing.T) {
	f := nativeHistoryFixture(t, 1, 0)
	f.write(t, History, func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO thread_turns VALUES('task','failed-turn','failed','{"message":"Native failure"}',2,1)`); err != nil {
			t.Fatal(err)
		}
	})
	page := f.read(t, "")
	if len(page.Messages) != 2 || page.Messages[1].ID != "filo-turn-error:failed-turn" || page.Messages[1].Text != "Native failure" || page.Messages[1].Timestamp != 2000 || !page.Messages[1].Error.Value {
		t.Fatal(page.Messages)
	}
	if len(f.read(t, "", "failed-turn").Messages) != 1 {
		t.Fatal("Desktop-covered error duplicated")
	}
	if len(f.read(t, IndexedHistoryCursor).Messages) != 1 {
		t.Fatal("Older page repeated terminal error")
	}
}

func TestCatalogPagesUseStableNativeOrderAndArchivedFilter(t *testing.T) {
	f := nativeHistoryFixture(t, 1, 0)
	f.write(t, Catalog, func(db *sql.DB) {
		for i := 0; i < 35; i++ {
			if _, err := db.Exec("INSERT INTO threads VALUES(?,NULL,?,?,2,0,?,NULL,NULL)", fmt.Sprintf("task-%02d", i), fmt.Sprint("Title ", i), f.home, f.path); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec("INSERT INTO threads VALUES('archived','Archived','Old',?,99,1,?,NULL,NULL)", f.home, f.path); err != nil {
			t.Fatal(err)
		}
	})
	first, err := f.history.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sessions) != 30 || first.Sessions[0].ID != "task-34" || first.Sessions[0].Title != "Title 34" || !first.Sessions[0].Status.Known || first.Sessions[0].Status.Value != nil {
		t.Fatal(first)
	}
	second, err := f.history.List(context.Background(), *first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Sessions) != 6 || second.NextCursor != nil || second.Sessions[5].Title != "Task" {
		t.Fatal(second)
	}
	for _, cursor := range []string{"wrong", encodeCursor("list", map[string]any{"at": 0.5, "id": "x"}), encodeCursor("list", map[string]any{"at": 2})} {
		if _, err := f.history.List(context.Background(), cursor); err == nil {
			t.Fatal("Malformed list cursor accepted")
		}
	}
}
