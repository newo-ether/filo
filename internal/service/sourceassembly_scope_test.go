package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/newo-ether/filo/internal/codex"
)

func assemblyNativeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE threads(id TEXT,name TEXT,title TEXT,cwd TEXT,updated_at INTEGER,archived INTEGER,rollout_path TEXT,model TEXT,reasoning_effort TEXT);INSERT INTO threads(id,name) VALUES('fixture','Fixture')`)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("fixture catalog: %v %v", err, closeErr)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"OPENAI_API_KEY":"fixture-not-real"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestAssemblyDaemonRequiresPeerAndActualStoreIdentity(t *testing.T) {
	home, other := assemblyNativeHome(t), assemblyNativeHome(t)
	sid := "S-1-5-21-1-2-3-1001"
	verify := verifySourceDaemon(sid, func(context.Context) (string, error) { return home, nil }, StaticUserWorkerToken(testToken))
	ctx := context.Background()
	if err := verify(ctx, codex.LocalDaemonIdentity{UserID: sid, CodexHome: home}); err != nil {
		t.Fatal(err)
	}
	for _, peer := range []codex.LocalDaemonIdentity{
		{UserID: "S-1-5-18", CodexHome: home}, {UserID: sid, CodexHome: other}, {UserID: sid, CodexHome: "relative"},
	} {
		if err := verify(ctx, peer); err == nil {
			t.Fatal("unverified daemon admitted")
		}
	}
	before, err := codexSourceScope(sid, func(context.Context) (string, error) { return home, nil }, StaticUserWorkerToken(testToken))(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"OPENAI_API_KEY":"fixture-account-b"}`), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := codexSourceScope(sid, func(context.Context) (string, error) { return home, nil }, StaticUserWorkerToken(testToken))(ctx)
	if err != nil || before == after {
		t.Fatal("account change kept an old native scope")
	}
}
