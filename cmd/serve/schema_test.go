package serve

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// applyMigrations runs the Up section of every goose migration, in order.
func applyMigrations(t *testing.T, db *sql.DB) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	slices.Sort(files)
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		up, _, found := strings.Cut(string(b), "-- +goose Down")
		if !found || !strings.Contains(up, "-- +goose Up") {
			t.Fatalf("%s: no goose Up and Down sections", file)
		}
		if _, err := db.Exec(up); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
}

// describeTable returns the table's columns, and whether its key is
// AUTOINCREMENT, which PRAGMA table_info doesn't report.
func describeTable(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, fmt.Sprintf("%s %s notnull=%d default=%v pk=%d", name, typ, notNull, dflt.String, pk))
	}
	if len(columns) == 0 {
		t.Fatalf("table %s doesn't exist", table)
	}

	var ddl string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	columns = append(columns, fmt.Sprintf("autoincrement=%v", strings.Contains(strings.ToUpper(ddl), "AUTOINCREMENT")))

	rows, err = db.Query("SELECT name, \"unique\" FROM pragma_index_list(?) ORDER BY name", table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var unique int
		if err := rows.Scan(&name, &unique); err != nil {
			t.Fatal(err)
		}
		// indexes are compared by what they cover, since the migrations
		// create them under a different table name
		var cols []string
		idx, err := db.Query("SELECT name FROM pragma_index_info(?)", name)
		if err != nil {
			t.Fatal(err)
		}
		for idx.Next() {
			var col string
			idx.Scan(&col)
			cols = append(cols, col)
		}
		idx.Close()
		columns = append(columns, fmt.Sprintf("index(%s) unique=%d", strings.Join(cols, ","), unique))
	}
	return columns
}

// TestSchemaMatchesMigrations checks that testSchema, which the tests use,
// matches what the migrations create.
func TestSchemaMatchesMigrations(t *testing.T) {
	migrated, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "migrated.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	applyMigrations(t, migrated)

	test, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	if _, err := test.Exec(testSchema); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"upstreams", "iris_auth"} {
		want, got := describeTable(t, migrated, table), describeTable(t, test, table)
		if !slices.Equal(want, got) {
			t.Errorf("%s differs from the migrations:\nmigrations: %q\ntestSchema: %q", table, want, got)
		}
	}
}
