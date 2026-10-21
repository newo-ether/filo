package nativescope

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthFingerprintBoundAndCancellationDuringRead(t *testing.T) {
	home := fixtureHome(t)
	prefix, suffix := `{"OPENAI_API_KEY":"`, `"}`
	writeFixtureAuth(t, home, prefix+strings.Repeat("x", maxAuthBytes-len(prefix)-len(suffix))+suffix)
	if _, err := readAuth(context.Background(), filepath.Join(home, "auth.json")); err != nil {
		t.Fatal("Exact bounded valid auth rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader := &contextReader{ctx: ctx, reader: &cancellingReader{cancel: cancel}}
	if _, err := io.ReadAll(reader); !errors.Is(err, context.Canceled) {
		t.Fatal("Cancellation during bounded auth read was ignored")
	}
}

type cancellingReader struct {
	cancel context.CancelFunc
}

func (reader *cancellingReader) Read(buffer []byte) (int, error) {
	reader.cancel()
	buffer[0] = 'x'
	return 1, nil
}

func TestActualDeadlineAndMissingStoreReturnNoIdentity(t *testing.T) {
	home := fixtureHome(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	if value, err := Codex(ctx, fixtureKey(), fixtureSID, home); value != "" || err != context.DeadlineExceeded {
		t.Fatal("Expired scope request did not retain deadline error")
	}
	if err := os.Remove(filepath.Join(home, "state_5.sqlite")); err != nil {
		t.Fatal(err)
	}
	if value, err := Codex(context.Background(), fixtureKey(), fixtureSID, home); value != "" || err != ErrUnavailable {
		t.Fatal("Missing catalog invented canonical identity")
	}
}

func TestAuthSymlinkFailsClosed(t *testing.T) {
	home := fixtureHome(t)
	path := filepath.Join(home, "auth.json")
	retained := filepath.Join(home, "fixture-auth")
	if err := os.Rename(path, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(retained, path); err != nil {
		t.Skip("Symlink fixtures require platform support")
	}
	if value, err := Codex(context.Background(), fixtureKey(), fixtureSID, home); value != "" || err != ErrUnavailable {
		t.Fatal("Symlink credential source accepted")
	}
}

func TestDuplicateCatalogVersionsNeverAlias(t *testing.T) {
	home := fixtureHome(t)
	fixtureCatalog(t, filepath.Join(home, "state_005.sqlite"))
	if value, err := Codex(context.Background(), fixtureKey(), fixtureSID, home); value != "" || err != ErrUnavailable {
		t.Fatal("Ambiguous native catalog granted canonical sharing")
	}
}
