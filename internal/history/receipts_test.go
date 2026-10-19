package history

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"
)

var receiptProbeSequence atomic.Uint64

func TestIndexedReceiptQueryReadsOnlyBoundedCandidateBodies(t *testing.T) {
	name := fmt.Sprintf("filo_receipt_probe_%d", receiptProbeSequence.Add(1))
	var reads atomic.Int64
	if err := sqlite.RegisterScalarFunction(name, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		reads.Add(1)
		return args[0], nil
	}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// The view counts every body evaluation by the real production query.
	// Candidate keys remain ordinary columns, including with no ordering index.
	for _, query := range []string{
		`CREATE TABLE backing(thread_id TEXT,turn_id TEXT,item_id TEXT,rollout_ordinal INTEGER,body TEXT)`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<512)
		 INSERT INTO backing SELECT 'task','turn',printf('item-%04d',i),i,
		 json_object('id',printf('item-%04d',i),'type','userMessage','clientId',printf('client-%04d',i)) FROM n`,
		`CREATE VIEW thread_items AS SELECT thread_id,turn_id,item_id,rollout_ordinal,` + name + `(body) AS item_json FROM backing`,
		`PRAGMA query_only=ON`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	receipts, err := indexedInputReceipts(context.Background(), tx, "task")
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != receiptCandidateLimit || receipts[0].ClientID != "client-0385" || receipts[len(receipts)-1].ClientID != "client-0512" {
		t.Fatalf("Candidate window = %+v", receipts)
	}
	// Each selected body is inspected once for its length and once for JSON.
	// An outer matching LIMIT or JSON filter would evaluate older bodies too.
	if got := reads.Load(); got != 2*receiptCandidateLimit {
		t.Fatalf("Receipt query evaluated %d bodies, want %d", got, 2*receiptCandidateLimit)
	}
}
