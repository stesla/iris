package password

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/stesla/iris/internal/auth"
	"github.com/stesla/iris/internal/database/dbtest"
)

// useDB points the db setting at path, and returns the database there.
func useDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	viper.Set("db", path)
	t.Cleanup(func() { viper.Set("db", "") })
	return db
}

// setStdin replaces standard input with a pipe holding input, which is never
// a terminal.
func setStdin(t *testing.T, input string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = io.WriteString(w, input)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
	})
	return r
}

func TestSetPassword(t *testing.T) {
	db := useDB(t, dbtest.NewFile(t))
	setStdin(t, "secret\n")

	require.NoError(t, SetPassword(&cobra.Command{}, nil))
	require.NoError(t, auth.CheckPassword(context.Background(), db, "secret"))
}

func TestSetPasswordNotMigrated(t *testing.T) {
	db := useDB(t, filepath.Join(t.TempDir(), "iris.db"))
	// a query, so the file is created
	_, err := db.Exec("PRAGMA user_version = 1")
	require.NoError(t, err)
	stdin := setStdin(t, "secret\n")

	err = SetPassword(&cobra.Command{}, nil)
	require.ErrorContains(t, err, "have the migrations been run?")

	// the check comes before the password is read
	line, err := io.ReadAll(stdin)
	require.NoError(t, err)
	require.Equal(t, "secret\n", string(line))
}

func TestSetPasswordMissingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	viper.Set("db", path)
	t.Cleanup(func() { viper.Set("db", "") })

	require.ErrorContains(t, SetPassword(&cobra.Command{}, nil), "opening database")
	require.NoFileExists(t, path)
}
