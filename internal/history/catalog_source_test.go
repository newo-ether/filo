package history

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func TestCatalogExcludesSubagentsBeforePagingAndKeepsUntitledChats(t *testing.T) {
	f := nativeHistoryFixture(t, 0, 0)
	f.write(t, Catalog, func(db *sql.DB) {
		if _, err := db.Exec("ALTER TABLE threads ADD COLUMN source TEXT"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("DELETE FROM threads"); err != nil {
			t.Fatal(err)
		}
		// Interleave subagents across pages; titled subagents must also be excluded.
		for i := 0; i < 65; i++ {
			id := fmt.Sprintf("chat-%03d", i)
			title := ""
			var source any = "vscode"
			if i%3 == 0 {
				source = nil
			}
			if i%3 == 1 {
				source = "{malformed"
			}
			if _, err := db.Exec("INSERT INTO threads(id,name,title,cwd,updated_at,archived,source) VALUES(?,?,?,?,?,0,?)", id, "", title, f.home, 1000-i, source); err != nil {
				t.Fatal(err)
			}
			for j, subagent := range []string{`{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}`, "subagent", `"subagent"`} {
				if _, err := db.Exec("INSERT INTO threads(id,name,title,cwd,updated_at,archived,source) VALUES(?,?,?,?,?,0,?)", fmt.Sprintf("child-%03d-%d", i, j), "Internal task", "", f.home, 1000-i, subagent); err != nil {
					t.Fatal(err)
				}
			}
		}
	})
	var cursor string
	seen := make(map[string]bool)
	for pageIndex, want := range []int{30, 30, 5} {
		page, err := f.history.List(context.Background(), cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Sessions) != want {
			t.Fatalf("Page %d contains %d rows, expected %d", pageIndex, len(page.Sessions), want)
		}
		for i, row := range page.Sessions {
			expected := fmt.Sprintf("chat-%03d", pageIndex*30+i)
			if row.ID != expected || seen[row.ID] || row.Title != "" {
				t.Fatalf("Catalog contains a subagent, missing row, or duplicate: id=%s expected=%s", row.ID, expected)
			}
			seen[row.ID] = true
		}
		if pageIndex < 2 {
			if page.NextCursor == nil {
				t.Fatal("Missing next-page cursor")
			}
			cursor = *page.NextCursor
		} else if page.NextCursor != nil {
			t.Fatal("Unexpected cursor on the last page")
		}
	}
}
