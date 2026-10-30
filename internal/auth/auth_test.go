package auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

const testSchema = `CREATE TABLE iris_auth (
	id     INTEGER PRIMARY KEY CHECK (id = 1),
	bcrypt TEXT NOT NULL
)`

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(testSchema)
	require.NoError(t, err)
	return db
}

func TestPasswordNotSet(t *testing.T) {
	ctx, db := context.Background(), newTestDB(t)

	set, err := PasswordIsSet(ctx, db)
	require.NoError(t, err)
	require.False(t, set)

	require.ErrorIs(t, CheckPassword(ctx, db, "anything"), ErrPasswordNotSet)
}

func TestSetAndCheckPassword(t *testing.T) {
	ctx, db := context.Background(), newTestDB(t)
	require.NoError(t, SetPassword(ctx, db, "secret"))

	set, err := PasswordIsSet(ctx, db)
	require.NoError(t, err)
	require.True(t, set)

	require.NoError(t, CheckPassword(ctx, db, "secret"))
	require.ErrorIs(t, CheckPassword(ctx, db, "wrong"), bcrypt.ErrMismatchedHashAndPassword)
	require.ErrorIs(t, CheckPassword(ctx, db, ""), bcrypt.ErrMismatchedHashAndPassword)

	var hash string
	require.NoError(t, db.QueryRow("SELECT bcrypt FROM iris_auth").Scan(&hash))
	require.NotContains(t, hash, "secret")
}

func TestChangePassword(t *testing.T) {
	ctx, db := context.Background(), newTestDB(t)
	require.NoError(t, SetPassword(ctx, db, "old"))
	require.NoError(t, SetPassword(ctx, db, "new"))

	require.NoError(t, CheckPassword(ctx, db, "new"))
	require.ErrorIs(t, CheckPassword(ctx, db, "old"), bcrypt.ErrMismatchedHashAndPassword)

	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM iris_auth").Scan(&n))
	require.Equal(t, 1, n)
}

func TestSetPasswordInvalid(t *testing.T) {
	ctx, db := context.Background(), newTestDB(t)
	require.Error(t, SetPassword(ctx, db, ""))
	require.ErrorIs(t, SetPassword(ctx, db, strings.Repeat("x", 73)), bcrypt.ErrPasswordTooLong)

	set, err := PasswordIsSet(ctx, db)
	require.NoError(t, err)
	require.False(t, set)
}
