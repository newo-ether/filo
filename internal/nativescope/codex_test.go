package nativescope

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const fixtureSID = "S-1-5-21-101-202-303-1001"
const fixtureAuth = `{"OPENAI_API_KEY":"fixture-account-a-not-a-real-key"}`
const fixtureSchema = `CREATE TABLE threads(id TEXT,name TEXT,title TEXT,cwd TEXT,updated_at INTEGER,archived INTEGER,rollout_path TEXT,model TEXT,reasoning_effort TEXT)`

func fixtureKey() []byte { return bytes.Repeat([]byte{0x5c}, 32) }

func fixtureCatalog(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fixtureSchema + `;INSERT INTO threads(id,name) VALUES('same-native-id','Fixture')`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func fixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	fixtureCatalog(t, filepath.Join(home, "state_5.sqlite"))
	writeFixtureAuth(t, home, fixtureAuth)
	return home
}

func writeFixtureAuth(t *testing.T, home, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func scope(t *testing.T, home string) string {
	t.Helper()
	value, err := Codex(context.Background(), fixtureKey(), fixtureSID, home)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^nsc1_[A-Za-z0-9_-]{43}$`).MatchString(value) || len(value) > 128 {
		t.Fatal("Scope is not a bounded opaque identifier")
	}
	for _, private := range []string{fixtureSID, home, "fixture-account", "auth.json", "state_5.sqlite"} {
		if strings.Contains(value, private) {
			t.Fatal("Scope exposed private identity material")
		}
	}
	return value
}

func TestDesktopCLIShareOnlyTheSameVerifiedAccountAndActualStore(t *testing.T) {
	home := fixtureHome(t)
	desktop := scope(t, home)
	if cli := scope(t, home); cli != desktop {
		t.Fatal("Same native store/account changed across calls or access paths")
	}
	// A path alias of the same actual file must share; a byte-for-byte copy must not.
	alias := t.TempDir()
	if err := os.Link(filepath.Join(home, "state_5.sqlite"), filepath.Join(alias, "state_5.sqlite")); err != nil {
		t.Fatal(err)
	}
	writeFixtureAuth(t, alias, fixtureAuth)
	if scope(t, alias) != desktop {
		t.Fatal("Filesystem identity was replaced by path spelling")
	}
	copyHome := fixtureHome(t)
	if scope(t, copyHome) == desktop {
		t.Fatal("Equal native thread IDs and account bytes merged different stores")
	}
	writeFixtureAuth(t, alias, `{"tokens":{"access_token":"fixture-account-b"}}`)
	if scope(t, alias) == desktop {
		t.Fatal("Same file and thread IDs merged different accounts")
	}
}

func TestScopeChangesOnSamePathStoreReplacementAndNotCatalogMutation(t *testing.T) {
	home := fixtureHome(t)
	original := scope(t, home)
	path := filepath.Join(home, "state_5.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE threads SET title='Native writer changed title'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if scope(t, home) != original {
		t.Fatal("Ordinary native catalog writes changed scope")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(home, "retained-original")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if scope(t, home) == original {
		t.Fatal("Same-path replacement inherited previous store identity")
	}
}

func TestScopeRecomputesAuthSIDAndPrivateWorkerKey(t *testing.T) {
	home := fixtureHome(t)
	original := scope(t, home)
	writeFixtureAuth(t, home, `{"tokens":{"id_token":"fixture-account-b"}}`)
	if scope(t, home) == original {
		t.Fatal("Account change retained scope")
	}
	writeFixtureAuth(t, home, fixtureAuth)
	if scope(t, home) != original {
		t.Fatal("Same account bytes and store did not recover their stable scope")
	}
	for _, input := range []struct {
		key []byte
		sid string
	}{
		{bytes.Repeat([]byte{0x4a}, 32), fixtureSID},
		{fixtureKey(), "S-1-5-21-101-202-303-1002"},
	} {
		value, err := Codex(context.Background(), input.key, input.sid, home)
		if err != nil || value == original {
			t.Fatal("Different worker key or selected user merged scopes")
		}
	}
	// The conservative raw fingerprint intentionally rotates for formatting/token rotation.
	writeFixtureAuth(t, home, fixtureAuth+"\n")
	if scope(t, home) == original {
		t.Fatal("Auth rotation was cached")
	}
}

func TestMissingInvalidAndOversizedAuthNeverInventsAScope(t *testing.T) {
	home := fixtureHome(t)
	path := filepath.Join(home, "auth.json")
	for _, auth := range []string{"", "null", "{}", "[]", `{"OPENAI_API_KEY":null}`, `{"tokens":{"access_token":" "}}`, "not-json",
		`{"OPENAI_API_KEY":"` + strings.Repeat("x", maxAuthBytes) + `"}`} {
		writeFixtureAuth(t, home, auth)
		value, err := Codex(context.Background(), fixtureKey(), fixtureSID, home)
		if value != "" || !errors.Is(err, ErrUnavailable) {
			t.Fatal("Invalid auth did not return an opaque unavailable error")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if value, err := Codex(context.Background(), fixtureKey(), fixtureSID, home); value != "" || err != ErrUnavailable {
		t.Fatal("Missing auth invented canonical sharing")
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Codex(context.Background(), fixtureKey(), fixtureSID, home); err != ErrUnavailable {
		t.Fatal("Non-regular auth input accepted")
	}
}

func TestCatalogDiscoveryUsesHighestFamilyWithoutStaleFallback(t *testing.T) {
	home := fixtureHome(t)
	old := scope(t, home)
	latest := filepath.Join(home, "state_9007199254740993.sqlite")
	if err := os.WriteFile(latest, []byte("corrupt highest catalog"), 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := Codex(context.Background(), fixtureKey(), fixtureSID, home); value != "" || err != ErrUnavailable {
		t.Fatal("Corrupt highest catalog fell back")
	}
	if err := os.Remove(latest); err != nil {
		t.Fatal(err)
	}
	fixtureCatalog(t, latest)
	if scope(t, home) == old {
		t.Fatal("Highest compatible catalog was not rediscovered")
	}
	if err := os.Rename(latest, filepath.Join(home, "retained-highest")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(latest, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Codex(context.Background(), fixtureKey(), fixtureSID, home); err != ErrUnavailable {
		t.Fatal("Non-file highest catalog fell back")
	}
}

func TestScopeReadsDoNotMutateNativeFilesOrCreateSidecars(t *testing.T) {
	home := fixtureHome(t)
	before := snapshot(t, home)
	for range 3 {
		_ = scope(t, home)
	}
	after := snapshot(t, home)
	if len(before) != len(after) {
		t.Fatal("Identity reads created or removed native files")
	}
	for name, digest := range before {
		if after[name] != digest {
			t.Fatal("Identity reads changed native file bytes")
		}
	}
}

func snapshot(t *testing.T, home string) map[string][32]byte {
	t.Helper()
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][32]byte)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(home, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = sha256.Sum256(data)
	}
	return result
}

func TestCancellationInvalidInputsAndUnavailableErrorsArePrivate(t *testing.T) {
	home := fixtureHome(t)
	for _, ctx := range []context.Context{cancelledContext(), expiredContext()} {
		if value, err := Codex(ctx, fixtureKey(), fixtureSID, home); value != "" || !errors.Is(err, ctx.Err()) {
			t.Fatal("Cancelled or expired request performed discovery")
		}
	}
	for _, input := range []struct {
		key  []byte
		sid  string
		home string
	}{
		{nil, fixtureSID, home}, {[]byte("short"), fixtureSID, home},
		{bytes.Repeat([]byte{1}, 4097), fixtureSID, home}, {fixtureKey(), "", home},
		{fixtureKey(), "private-account-name", home}, {fixtureKey(), fixtureSID, ""},
		{fixtureKey(), fixtureSID, "relative-home"}, {fixtureKey(), fixtureSID, filepath.Join(t.TempDir(), "private-missing")},
	} {
		value, err := Codex(context.Background(), input.key, input.sid, input.home)
		if value != "" || err != ErrUnavailable {
			t.Fatal("Invalid input returned private details or a scope")
		}
	}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func expiredContext() context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	cancel()
	return ctx
}
