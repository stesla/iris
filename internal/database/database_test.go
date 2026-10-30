package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stesla/iris/internal/database/dbtest"
)

// newEmptyFile creates a database with no tables at a temporary path.
func newEmptyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "iris.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer db.Close()
	// a query, so the file is created
	_, err = db.Exec("PRAGMA user_version = 1")
	require.NoError(t, err)
	return path
}

func TestOpen(t *testing.T) {
	db, err := Open(context.Background(), dbtest.NewFile(t))
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func TestOpenNotMigrated(t *testing.T) {
	_, err := Open(context.Background(), newEmptyFile(t))
	require.ErrorContains(t, err, "have the migrations been run?")
}

func TestOpenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	_, err := Open(context.Background(), path)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoFileExists(t, path)
}

func TestOpenNotADatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iris.db")
	require.NoError(t, os.WriteFile(path, []byte("not a database, but long enough to have a header"), 0o600))
	_, err := Open(context.Background(), path)
	require.ErrorContains(t, err, "opening database")
}
