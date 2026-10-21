package history

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityFileUsesSharedDiscoveryAndReadOnlyHandle(t *testing.T) {
	home := t.TempDir()
	old := filepath.Join(home, "state_5.sqlite")
	latest := filepath.Join(home, "state_23.sqlite")
	databaseFixture(t, old, catalogSchema, "Old")
	databaseFixture(t, latest, catalogSchema, "Latest")
	before := fileHash(t, latest)
	file, err := OpenIdentityFile(context.Background(), home, Catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := os.Stat(latest)
	if err != nil || !os.SameFile(info, selected) {
		t.Fatal("Identity did not select the ordinary catalog owner")
	}
	if _, err := file.Write([]byte("forbidden")); err == nil {
		t.Fatal("Identity handle permits native writes")
	}
	if fileHash(t, latest) != before {
		t.Fatal("Identity handle changed native data")
	}
	// Keeping the identity handle alive must not prevent the native application
	// from replacing its own catalog at the same path.
	if err := os.Rename(latest, filepath.Join(home, "retained-original")); err != nil {
		t.Fatalf("Identity handle blocked native replacement: %v", err)
	}
	databaseFixture(t, latest, catalogSchema, "Replaced")
	current, err := OpenIdentityFile(context.Background(), home, Catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	replacement, err := current.Stat()
	if err != nil || os.SameFile(info, replacement) {
		t.Fatal("Same-path replacement retained old file identity")
	}
}

func TestIdentityFileRejectsHighestIncompatibleAndCancellation(t *testing.T) {
	home := t.TempDir()
	databaseFixture(t, filepath.Join(home, "state_5.sqlite"), catalogSchema, "Old")
	latest := filepath.Join(home, "state_100.sqlite")
	databaseFixture(t, latest, "CREATE TABLE unrelated(value TEXT)", "")
	if file, err := OpenIdentityFile(context.Background(), home, Catalog); err == nil {
		file.Close()
		t.Fatal("Identity fell back from highest incompatible database")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if file, err := OpenIdentityFile(ctx, home, Catalog); file != nil || err != context.Canceled {
		t.Fatal("Cancelled identity request did not stop")
	}
}
