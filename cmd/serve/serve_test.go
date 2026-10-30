package serve

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
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

// captureLogger points logger at the returned buffer until the test ends.
func captureLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	saved := logger
	t.Cleanup(func() { logger = saved })
	var buf bytes.Buffer
	logger = zerolog.New(&buf)
	return &buf
}

func TestSetLogLevel(t *testing.T) {
	buf := captureLogger(t)
	if err := setLogLevel("warn"); err != nil {
		t.Fatal(err)
	}

	logger.Info().Msg("dropped")
	logger.Warn().Msg("kept")

	if got := buf.String(); strings.Contains(got, "dropped") || !strings.Contains(got, "kept") {
		t.Errorf("got %q", got)
	}
}

func TestSetLogLevelInvalid(t *testing.T) {
	for _, level := range []string{"bogus", ""} {
		buf := captureLogger(t)
		if err := setLogLevel(level); err == nil {
			t.Errorf("level %q: no error", level)
		}

		logger.Info().Msg("kept")
		if got := buf.String(); !strings.Contains(got, "kept") {
			t.Errorf("level %q: got %q", level, got)
		}
	}
}

func TestSessionPoolInheritsLogLevel(t *testing.T) {
	buf := captureLogger(t)
	if err := setLogLevel("warn"); err != nil {
		t.Fatal(err)
	}

	p := NewSessionPool(nil, nil, logger)
	p.logger.Info().Msg("dropped")
	p.logger.Warn().Msg("kept")

	if got := buf.String(); strings.Contains(got, "dropped") || !strings.Contains(got, "kept") {
		t.Errorf("got %q", got)
	}
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
