package history

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestHistoryRuntimeBoundsNeverSettleFromStaleIndex(t *testing.T) {
	for _, bound := range []string{"records", "bytes", "unfinished"} {
		t.Run(bound, func(t *testing.T) {
			f := nativeHistoryFixture(t, 1, 0)
			f.write(t, History, func(db *sql.DB) {
				if _, err := db.Exec(`INSERT INTO thread_turns VALUES('task','old-turn','completed',NULL,1,1)`); err != nil {
					t.Fatal(err)
				}
			})
			appendRollout(t, f.path, `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"old-turn"}}`+"\n"+
				`{"type":"event_msg","payload":{"type":"task_started","turn_id":"new-turn"}}`+"\n")
			switch bound {
			case "records":
				appendRollout(t, f.path, strings.Repeat("{}\n", 4096))
			case "bytes":
				appendRollout(t, f.path, strings.Repeat("x", 8*1024*1024+1)+"\n")
			case "unfinished":
				appendRollout(t, f.path, strings.Repeat("x", 8*1024*1024+1))
			}
			// The public history owner still calls indexedCompletion after its
			// runtime read. Even an index agreeing with itself cannot settle.
			page := f.read(t, IndexedHistoryCursor)
			if page.Runtime.Status != "notLoaded" || page.Runtime.CompletedTurnID.Known || page.Runtime.ActiveTurnID != nil {
				t.Fatalf("%s bound invented completion: %+v", bound, page.Runtime)
			}
		})
	}
}

func TestIndexedReceiptsBoundCandidatesAndBodiesWithoutInventingAcceptance(t *testing.T) {
	f := nativeHistoryFixture(t, 1, 0)
	f.write(t, History, func(db *sql.DB) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		insert := func(id string, ordinal int, body string) {
			t.Helper()
			if _, err := tx.Exec("INSERT INTO thread_items VALUES(?,?,?,?,?,?)", "task", "old-turn", id, ordinal, 0, body); err != nil {
				t.Fatal(err)
			}
		}
		insert("too-old", -1, `{"id":"too-old","type":"userMessage","clientId":"old-client"}`)
		for index := 1; index <= receiptCandidateLimit; index++ {
			id := fmt.Sprint("tool-", index)
			insert(id, index, `{"id":"`+id+`","type":"commandExecution","aggregatedOutput":"`+strings.Repeat("x", 8192)+`"}`)
		}
		insert("oversized", receiptCandidateLimit+1, `{"id":"oversized","type":"userMessage","clientId":"large-client","content":"`+strings.Repeat("x", receiptBodyBytes)+`"}`)
		insert("accepted", receiptCandidateLimit+2, `{"id":"accepted","type":"userMessage","clientId":"accepted-client"}`)
		insert("mismatched", receiptCandidateLimit+3, `{"id":"foreign","type":"userMessage","clientId":"foreign-client"}`)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	})
	// A stored receipt may be independent of the visible page. Skipping any
	// unverified candidate never manufactures a rejection or an ACK.
	page := f.read(t, IndexedHistoryCursor, "old-turn")
	if len(page.InputReceipts) != 1 || page.InputReceipts[0].ClientID != "accepted-client" || page.InputReceipts[0].MessageID != "accepted" {
		t.Fatalf("bounded receipts = %+v", page.InputReceipts)
	}
	db, err := OpenDatabase(context.Background(), f.home, History)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM thread_items"); err == nil {
		t.Fatal("Receipt read connection allowed native writes")
	}
}
