package history

import (
	"context"
	"database/sql"
	"testing"
)

func TestNativeTitleRefreshAndEmptyNameFallback(t *testing.T) {
	f := nativeHistoryFixture(t, 1, 1)
	for _, title := range []string{"Generated title", "Renamed natively"} {
		f.write(t, Catalog, func(db *sql.DB) {
			if _, err := db.Exec("UPDATE threads SET name='',title=? WHERE id='task'", title); err != nil {
				t.Fatal(err)
			}
		})
		listed, err := f.history.List(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		page := f.read(t, "")
		if string(listed.Sessions[0].Title) != title || page.Runtime.Title == nil || string(*page.Runtime.Title) != title {
			t.Fatalf("native title did not refresh: list=%q runtime=%v", listed.Sessions[0].Title, page.Runtime.Title)
		}
	}
}
