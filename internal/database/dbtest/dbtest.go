// Package dbtest creates databases for tests by applying the real
// migrations, so tests never use a schema that differs from production.
package dbtest

import (
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/stesla/iris/migrations"
)

// New returns a migrated database in a temporary directory, closed when the
// test ends.
func New(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", NewFile(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// NewFile creates a migrated database in a temporary directory and returns
// its path.
func NewFile(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "iris.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Up(db); err != nil {
		t.Fatal(err)
	}
	return path
}

// Up applies the Up section of every migration, in order.
func Up(db *sql.DB) error {
	files, err := files()
	if err != nil {
		return err
	}
	for _, f := range files {
		if _, err := db.Exec(f.up); err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
	}
	return nil
}

// Down applies the Down section of every migration, in reverse order.
func Down(db *sql.DB) error {
	files, err := files()
	if err != nil {
		return err
	}
	slices.Reverse(files)
	for _, f := range files {
		if _, err := db.Exec(f.down); err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
	}
	return nil
}

type migration struct {
	name, up, down string
}

// files reads the migrations, sorted by name. Each is split on its goose
// annotations and run as one batch, which is why annotations that need
// goose itself are rejected rather than ignored.
func files() ([]migration, error) {
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no migrations found")
	}
	slices.Sort(names)
	var result []migration
	for _, name := range names {
		b, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return nil, err
		}
		text := string(b)
		for _, unsupported := range []string{"+goose StatementBegin", "+goose NO TRANSACTION", "+goose ENVSUB"} {
			if strings.Contains(text, unsupported) {
				return nil, fmt.Errorf("%s: dbtest can't apply %q", name, unsupported)
			}
		}
		up, down, found := strings.Cut(text, "-- +goose Down")
		if !found || !strings.Contains(up, "-- +goose Up") {
			return nil, fmt.Errorf("%s: no goose Up and Down sections", name)
		}
		result = append(result, migration{name, up, down})
	}
	return result, nil
}
