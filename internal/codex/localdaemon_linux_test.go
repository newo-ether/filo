//go:build linux

package codex

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func daemonFixtureDirectory(t *testing.T) (string, string, string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("UDS fixture requires an ordinary selected-user uid")
	}
	root, err := os.MkdirTemp("", "fuds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	return private, root, strconv.Itoa(os.Geteuid())
}
