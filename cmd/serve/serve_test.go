package serve

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/stesla/iris/internal/database/dbtest"
)

// newTestDBFile returns the path of a migrated database with the Iris
// password set to testIrisPassword.
func newTestDBFile(t *testing.T) string {
	t.Helper()
	path := dbtest.NewFile(t)
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	setTestPassword(t, db)
	return path
}

// usePrepareSettings points the db setting at path, and secret.key_file at a
// key file in a temporary directory, which it returns.
func usePrepareSettings(t *testing.T, path string) string {
	t.Helper()
	keyFile := filepath.Join(t.TempDir(), "iris.key")
	viper.Set("db", path)
	viper.Set("secret.key_file", keyFile)
	t.Cleanup(func() {
		viper.Set("db", "")
		viper.Set("secret.key_file", "")
	})
	return keyFile
}

func TestPrepare(t *testing.T) {
	keyFile := usePrepareSettings(t, newTestDBFile(t))

	db, box, err := prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if box == nil {
		t.Fatal("no box")
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Errorf("key file wasn't created: %v", err)
	}
}

func TestPrepareWithoutIrisPassword(t *testing.T) {
	keyFile := usePrepareSettings(t, dbtest.NewFile(t))

	_, _, err := prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), "run `iris password`") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Errorf("key file was created before the password check: %v", err)
	}
}

func TestPrepareMissingDatabase(t *testing.T) {
	usePrepareSettings(t, filepath.Join(t.TempDir(), "missing.db"))

	if _, _, err := prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "opening database") {
		t.Fatalf("got %v", err)
	}
}

func TestPrepareBadKeyFile(t *testing.T) {
	keyFile := usePrepareSettings(t, newTestDBFile(t))
	if err := os.WriteFile(keyFile, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "must contain") {
		t.Fatalf("got %v", err)
	}
}
