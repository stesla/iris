package dbtest

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func tables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func TestNew(t *testing.T) {
	got := tables(t, New(t))
	if len(got) != 2 || got[0] != "iris_auth" || got[1] != "upstreams" {
		t.Errorf("tables = %q", got)
	}
}

// TestDownThenUp checks that every migration can be rolled back, and that
// the migrations apply again afterwards.
func TestDownThenUp(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "iris.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Up(db); err != nil {
		t.Fatal(err)
	}
	if err := Down(db); err != nil {
		t.Fatal(err)
	}
	if got := tables(t, db); len(got) != 0 {
		t.Errorf("tables after Down = %q", got)
	}
	if err := Up(db); err != nil {
		t.Fatal(err)
	}
}
