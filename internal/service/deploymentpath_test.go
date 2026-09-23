package service

import (
	"path/filepath"
	"testing"
)

func TestPackagedExecutorFindsSiblingScriptsAndGuardian(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release with spaces")
	executable := filepath.Join(release, "tools", "filo.exe")
	if got := rootForExecutable(executable); got != release {
		t.Fatalf("packaged root = %q, want %q", got, release)
	}
	if got := rootForExecutable(filepath.Join(release, "filo")); got != release {
		t.Fatalf("flat development root = %q", got)
	}
}
