// Package history reads the selected original account's persisted native data.
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

type DatabaseKind string

const (
	Catalog DatabaseKind = "catalog"
	History DatabaseKind = "history"
)

type databaseFamily struct {
	filename *regexp.Regexp
	tables   map[string][]string
}

var databaseFamilies = map[DatabaseKind]databaseFamily{
	Catalog: {regexp.MustCompile(`^state(?:_(\d+))?\.sqlite$`), map[string][]string{
		"threads": {"id", "name", "title", "cwd", "updated_at", "archived", "rollout_path", "model", "reasoning_effort"},
	}},
	History: {regexp.MustCompile(`^thread_history(?:_(\d+))?\.sqlite$`), map[string][]string{
		"thread_items": {"thread_id", "turn_id", "item_id", "rollout_ordinal", "created_at_ms", "item_json"},
		"thread_turns": {"thread_id", "turn_id", "status", "error_json", "started_at", "rollout_ordinal"},
	}},
}

// OpenDatabase discovers afresh on every call. A newer incompatible database is
// an explicit failure, never permission to expose an older stale catalog.
func OpenDatabase(ctx context.Context, home string, kind DatabaseKind) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	family, ok := databaseFamilies[kind]
	if !ok {
		return nil, errors.New("Unknown native database family")
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		entry   os.DirEntry
		version *big.Int
	}
	var candidates []candidate
	for _, entry := range entries {
		match := family.filename.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version := new(big.Int)
		if match[1] != "" {
			version.SetString(match[1], 10)
		}
		candidates = append(candidates, candidate{entry, version})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].version.Cmp(candidates[j].version) > 0 })
	if len(candidates) == 0 {
		return nil, fmt.Errorf("Native %s database is unavailable", kind)
	}
	selected := candidates[0]
	if len(candidates) > 1 && selected.version.Cmp(candidates[1].version) == 0 {
		return nil, fmt.Errorf("Native %s database selection is ambiguous", kind)
	}
	info, err := selected.entry.Info()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Native %s database must be a regular file", kind)
	}
	dsn, err := readOnlyURI(filepath.Join(home, selected.entry.Name()))
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := validateSchema(ctx, db, kind, family); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func readOnlyURI(path string) (string, error) {
	path = strings.TrimPrefix(path, `\\?\`)
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.ToSlash(abs)
	if len(abs) > 1 && abs[1] == ':' {
		abs = "/" + abs
	}
	query := url.Values{"mode": {"ro"}, "_pragma": {"query_only=ON", "trusted_schema=OFF", "busy_timeout=1000"}}
	return (&url.URL{Scheme: "file", Path: abs, RawQuery: query.Encode()}).String(), nil
}

func validateSchema(ctx context.Context, db *sql.DB, kind DatabaseKind, family databaseFamily) error {
	for table, required := range family.tables {
		columns, err := tableColumns(ctx, db, table)
		if err != nil {
			return err
		}
		for _, column := range required {
			if !columns[column] {
				return fmt.Errorf("Native %s database schema is incompatible", kind)
			}
		}
	}
	return nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}
