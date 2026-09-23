package sourcebudget

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// moduleRoot resolves the repository root of this test, so the enforced sweep is
// the one of this checkout rather than of a fixture.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the source of this test")
	}
	directory := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("no module root above this test")
		}
		directory = parent
	}
}

// TestMaintainedSourceStaysWithinBudget is the gate itself: every maintained file
// of this repository is measured, and one oversized file fails the test with its
// measured count.
func TestMaintainedSourceStaysWithinBudget(t *testing.T) {
	report, err := Sweep(moduleRoot(t))
	if err != nil {
		t.Fatalf("sweep this repository: %v", err)
	}
	if report.Files == 0 {
		t.Fatal("the sweep measured no maintained file, so it cannot be enforcing anything")
	}
	if err := Check(moduleRoot(t)); err != nil {
		t.Fatal(err)
	}
}

// TestSourceBudgetCountsPhysicalLinesAcrossEndings pins the measurement: the
// budget covers carriage-return, line-feed and carriage-return line-feed endings
// alike, the ceiling is inclusive and one line more is a violation.
func TestSourceBudgetCountsPhysicalLinesAcrossEndings(t *testing.T) {
	root := t.TempDir()
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		body := strings.Repeat("line"+ending, MaximumLines)
		writeFixture(t, filepath.Join(root, "packages", "source.go"), body)
		if err := Check(root); err != nil {
			t.Fatalf("ending %q at the ceiling: %v", ending, err)
		}
		writeFixture(t, filepath.Join(root, "packages", "source.go"), body+"last")
		err := Check(root)
		if err == nil {
			t.Fatalf("ending %q one line above the ceiling was accepted", ending)
		}
		want := "packages/source.go: 801 physical lines (maximum 800)"
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ending %q reported %q, want %q", ending, err.Error(), want)
		}
	}
}

// TestSourceBudgetSkipsDependenciesGeneratedOutputAndForeignExtensions pins what
// the budget deliberately does not measure: a dependency, a build output and a
// file extension this repository does not maintain by hand. None of them may mask
// a violation or invent one.
func TestSourceBudgetSkipsDependenciesGeneratedOutputAndForeignExtensions(t *testing.T) {
	root := t.TempDir()
	oversized := strings.Repeat("line\n", MaximumLines*2)
	writeFixture(t, filepath.Join(root, "node_modules", "dependency.go"), oversized)
	writeFixture(t, filepath.Join(root, "dist", "output.ps1"), oversized)
	writeFixture(t, filepath.Join(root, ".harness", "runtime-logs", "log.md"), oversized)
	writeFixture(t, filepath.Join(root, "vendor", "thirdparty.cs"), oversized)
	writeFixture(t, filepath.Join(root, "packages", "generated.ts"), oversized)
	writeFixture(t, filepath.Join(root, "packages", "empty.go"), "")
	report, err := Sweep(root)
	if err != nil {
		t.Fatalf("sweep the fixture: %v", err)
	}
	if err := Check(root); err != nil {
		t.Fatalf("an excluded file was measured: %v", err)
	}
	if report.Files != 1 {
		t.Fatalf("measured %d files, want the one maintained file", report.Files)
	}
	if CountLines(nil) != 0 {
		t.Fatalf("an empty body occupies %d lines, want 0", CountLines(nil))
	}
}

// TestSourceBudgetReportsEveryViolationInPathOrder pins the diagnostic a build
// prints: every oversized file, sorted, so a failing check names all of its work.
func TestSourceBudgetReportsEveryViolationInPathOrder(t *testing.T) {
	root := t.TempDir()
	oversized := strings.Repeat("line\n", MaximumLines+1)
	writeFixture(t, filepath.Join(root, "packages", "second.go"), oversized)
	writeFixture(t, filepath.Join(root, "docs", "first.md"), oversized)
	report, err := Sweep(root)
	if err != nil {
		t.Fatalf("sweep the fixture: %v", err)
	}
	if len(report.Violations) != 2 {
		t.Fatalf("reported %d violations, want 2", len(report.Violations))
	}
	if report.Violations[0].Path != "docs/first.md" || report.Violations[1].Path != "packages/second.go" {
		t.Fatalf("violations = %v, want them in path order", report.Violations)
	}
	if err := Check(root); err == nil || !strings.Contains(err.Error(), "docs/first.md") {
		t.Fatalf("Check reported %v, want it to name the oversized files", err)
	}
}

// writeFixture writes one measured fixture file, creating its directory.
func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
