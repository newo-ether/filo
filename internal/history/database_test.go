package history

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const catalogSchema = `CREATE TABLE threads(id TEXT,name TEXT,title TEXT,cwd TEXT,updated_at INTEGER,archived INTEGER,rollout_path TEXT,model TEXT,reasoning_effort TEXT)`
const historySchema = `CREATE TABLE thread_items(thread_id TEXT,turn_id TEXT,item_id TEXT,rollout_ordinal INTEGER,created_at_ms INTEGER,item_json TEXT);
CREATE TABLE thread_turns(thread_id TEXT,turn_id TEXT,status TEXT,error_json TEXT,started_at INTEGER,rollout_ordinal INTEGER)`

func databaseFixture(t *testing.T, path, schema, name string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if name != "" {
		if _, err := db.Exec("INSERT INTO threads(id,name) VALUES('task',?)", name); err != nil {
			t.Fatal(err)
		}
	}
}

func readName(t *testing.T, home string) string {
	t.Helper()
	db, err := OpenDatabase(context.Background(), home, Catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow("SELECT name FROM threads WHERE id='task'").Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

func TestDatabaseDiscoveryFollowsHighestNumericFamilyAndPreservesOldFiles(t *testing.T) {
	home := t.TempDir()
	old := filepath.Join(home, "state_5.sqlite")
	databaseFixture(t, old, catalogSchema, "Old")
	before := fileHash(t, old)
	if readName(t, home) != "Old" {
		t.Fatal("Initial catalog changed")
	}
	databaseFixture(t, filepath.Join(home, "state_23.sqlite"), catalogSchema, "Upgraded")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(old, future, future); err != nil {
		t.Fatal(err)
	}
	if readName(t, home) != "Upgraded" {
		t.Fatal("Selected modification time instead of native family version")
	}
	databaseFixture(t, filepath.Join(home, "state_9007199254740993.sqlite"), catalogSchema, "Beyond JS integer")
	if readName(t, home) != "Beyond JS integer" {
		t.Fatal("Large native suffix was truncated")
	}
	if fileHash(t, old) != before {
		t.Fatal("Read changed original database bytes")
	}
}

func TestNewerCorruptOrIncompatibleDatabaseNeverFallsBackAndRepairRecovers(t *testing.T) {
	home := t.TempDir()
	old := filepath.Join(home, "state_5.sqlite")
	databaseFixture(t, old, catalogSchema, "Original")
	latest := filepath.Join(home, "state_100.sqlite")
	databaseFixture(t, latest, "CREATE TABLE unrelated(value TEXT)", "")
	if db, err := OpenDatabase(context.Background(), home, Catalog); err == nil {
		db.Close()
		t.Fatal("Incompatible highest version fell back")
	} else if !strings.Contains(err.Error(), "schema is incompatible") {
		t.Fatal(err)
	}
	if err := os.WriteFile(latest, []byte("corrupt database"), 0600); err != nil {
		t.Fatal(err)
	}
	if db, err := OpenDatabase(context.Background(), home, Catalog); err == nil {
		db.Close()
		t.Fatal("Corrupt highest version fell back")
	}
	data, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(latest, data, 0600); err != nil {
		t.Fatal(err)
	}
	if readName(t, home) != "Original" {
		t.Fatal("Native repair requires a service restart")
	}
}

func TestAmbiguousAndNonFileCandidatesRejectWithoutStaleFallback(t *testing.T) {
	home := t.TempDir()
	databaseFixture(t, filepath.Join(home, "state_5.sqlite"), catalogSchema, "Original")
	duplicate := filepath.Join(home, "state_005.sqlite")
	databaseFixture(t, duplicate, catalogSchema, "Ambiguous")
	if db, err := OpenDatabase(context.Background(), home, Catalog); err == nil {
		db.Close()
		t.Fatal("Ambiguous catalog accepted")
	} else if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatal(err)
	}
	if err := os.Remove(duplicate); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "state_100.sqlite"), 0700); err != nil {
		t.Fatal(err)
	}
	if db, err := OpenDatabase(context.Background(), home, Catalog); err == nil {
		db.Close()
		t.Fatal("Directory accepted as database")
	} else if !strings.Contains(err.Error(), "regular file") {
		t.Fatal(err)
	}
}

func TestReadOnlyConnectionCannotWriteEvenAfterReopenOrQueryOnlyClearing(t *testing.T) {
	home := filepath.Join(t.TempDir(), "native space#%")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "thread_history_1.sqlite")
	databaseFixture(t, path, historySchema, "")
	before := fileHash(t, path)
	db, err := OpenDatabase(context.Background(), home, History)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, reopen := range []bool{false, true} {
		if reopen {
			db.SetMaxIdleConns(0)
		}
		for pragma, want := range map[string]int{"query_only": 1, "trusted_schema": 0, "busy_timeout": 1000} {
			var value int
			if err := db.QueryRow("PRAGMA " + pragma).Scan(&value); err != nil || value != want {
				t.Fatalf("Lost %s: %d %v", pragma, value, err)
			}
		}
		if _, err := db.Exec("DELETE FROM thread_items"); err == nil {
			t.Fatal("Read-only connection mutated data")
		}
	}
	db.SetMaxIdleConns(1)
	connection, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), "PRAGMA query_only=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), "INSERT INTO thread_items(thread_id) VALUES('forbidden')"); err == nil {
		t.Fatal("Read-only open flag could be downgraded")
	}
	if fileHash(t, path) != before {
		t.Fatal("Read-only operations changed file bytes")
	}
}

func TestUnrelatedFilesGrantNoNativeDatabaseAccessAndCancellationStopsOpen(t *testing.T) {
	home := t.TempDir()
	databaseFixture(t, filepath.Join(home, "state_999.sqlite.backup"), catalogSchema, "Backup")
	if db, err := OpenDatabase(context.Background(), home, Catalog); err == nil {
		db.Close()
		t.Fatal("Unrelated file granted native access")
	} else if !strings.Contains(err.Error(), "unavailable") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if db, err := OpenDatabase(ctx, home, Catalog); err != context.Canceled {
		if db != nil {
			db.Close()
		}
		t.Fatal(err)
	}
	if db, err := OpenDatabase(context.Background(), home, "unknown"); err == nil {
		db.Close()
		t.Fatal("Unknown family accepted")
	}
}

func TestNativeWALWriterProgressesWhileReadSnapshotRemainsStable(t *testing.T) {
	home := t.TempDir()
	owner, err := sql.Open("sqlite", filepath.Join(home, "state_1.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.Exec("PRAGMA journal_mode=WAL;" + catalogSchema + ";INSERT INTO threads(id,name) VALUES('first','First')"); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenDatabase(context.Background(), home, Catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM threads").Scan(&count); err != nil || count != 1 {
		t.Fatalf("Initial snapshot: %d %v", count, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := owner.ExecContext(ctx, "INSERT INTO threads(id,name) VALUES('second','Second')"); err != nil {
		t.Fatalf("Read blocked independent native writer: %v", err)
	}
	if err := tx.QueryRow("SELECT count(*) FROM threads").Scan(&count); err != nil || count != 1 {
		t.Fatalf("Snapshot changed mid-read: %d %v", count, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRow("SELECT count(*) FROM threads").Scan(&count); err != nil || count != 2 {
		t.Fatalf("Next read did not see native append: %d %v", count, err)
	}
}
