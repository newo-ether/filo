// Package sourcebudget holds the maintained-source size budget of this
// repository. The gate was a Node script, `scripts/check-source-size.mjs`, that
// every build ran before it compiled anything. A delivery may contain no Node
// runtime, so the rule lives here instead and runs with the ordinary Go test
// command, which is also what a build script can ask for without a second
// toolchain.
//
// The budget is measured in physical lines, and only maintained source counts:
// generated output and unmodified dependencies are excluded by directory, and
// only the extensions this repository maintains by hand are measured at all.
package sourcebudget

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaximumLines is the largest maintained file this repository accepts. The
// working aim is lower; the enforced ceiling is this one.
const MaximumLines = 800

// maintainedExtensions are the extensions of hand-maintained source files. A
// generated file carries a generated extension or a generated directory, so it
// never reaches this set.
var maintainedExtensions = map[string]struct{}{
	".go":   {},
	".ps1":  {},
	".cs":   {},
	".md":   {},
	".yml":  {},
	".yaml": {},
}

// excludedDirectories are the directories whose contents this repository does not
// maintain by hand: version control, harness runtime logs, build output and
// downloaded dependencies.
var excludedDirectories = map[string]struct{}{
	".git":         {},
	".harness":     {},
	"dist":         {},
	"node_modules": {},
	"vendor":       {},
	"thirdparty":   {},
}

// File is one measured maintained source file.
type File struct {
	// Path is the file path relative to the swept root, with forward slashes.
	Path string
	// Lines is the number of physical lines the file occupies.
	Lines int
}

// Report is the outcome of one sweep of a root directory.
type Report struct {
	// Files is the number of maintained files that were measured.
	Files int
	// Violations are the measured files above the budget, sorted by path.
	Violations []File
}

// Sweep measures every maintained source file below root. A missing root or an
// unreadable maintained file is an error, never a silent pass: an unmeasured
// file is exactly the file that would exceed the budget unnoticed.
func Sweep(root string) (Report, error) {
	report := Report{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			if _, excluded := excludedDirectories[entry.Name()]; excluded {
				return fs.SkipDir
			}
			return nil
		}
		if _, maintained := maintainedExtensions[strings.ToLower(filepath.Ext(entry.Name()))]; !maintained {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		measured := File{Path: filepath.ToSlash(relative), Lines: CountLines(body)}
		report.Files++
		if measured.Lines > MaximumLines {
			report.Violations = append(report.Violations, measured)
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	sort.Slice(report.Violations, func(left, right int) bool {
		return report.Violations[left].Path < report.Violations[right].Path
	})
	return report, nil
}

// Check sweeps root and returns the error a build fails with when a maintained
// file exceeds the budget, or nil when the sweep is clean.
func Check(root string) error {
	report, err := Sweep(root)
	if err != nil {
		return err
	}
	if len(report.Violations) == 0 {
		return nil
	}
	lines := make([]string, 0, len(report.Violations))
	for _, violation := range report.Violations {
		lines = append(lines, fmt.Sprintf("%s: %d physical lines (maximum %d)", violation.Path, violation.Lines, MaximumLines))
	}
	return fmt.Errorf("maintained source size exceeded:\n%s", strings.Join(lines, "\n"))
}

// CountLines counts the physical lines of one file body. A body ends with the
// last character it holds, so a trailing terminator closes the line it belongs
// to instead of opening an empty one, and an empty body holds no line at all.
// Carriage return, line feed and a carriage-return line-feed pair each end one
// line, which is the rule the TypeScript gate applied.
func CountLines(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	breaks := bytes.Count(body, []byte{'\n'})
	breaks += bytes.Count(body, []byte{'\r'}) - bytes.Count(body, []byte("\r\n"))
	if body[len(body)-1] == '\n' || body[len(body)-1] == '\r' {
		return breaks
	}
	return breaks + 1
}
