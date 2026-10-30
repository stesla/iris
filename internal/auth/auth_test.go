package auth

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/stesla/iris/internal/database/dbtest"
)

// newTestDB returns a migrated database, and lowers the bcrypt cost for the
// test so it stays fast.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	old := cost
	cost = bcrypt.MinCost
	t.Cleanup(func() { cost = old })
	return dbtest.New(t)
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
	require.NoError(t, db.QueryRow("SELECT password_hash FROM iris_auth").Scan(&hash))
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

func TestSetPasswordUsesDefaultCost(t *testing.T) {
	ctx, db := context.Background(), dbtest.New(t)
	require.NoError(t, SetPassword(ctx, db, "secret"))

	var hash string
	require.NoError(t, db.QueryRow("SELECT password_hash FROM iris_auth").Scan(&hash))
	got, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	require.Equal(t, bcrypt.DefaultCost, got)
}
