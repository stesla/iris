// Package auth manages the Iris password, which clients use to log in to Iris
// itself. It is separate from the upstream passwords Iris uses to log in to
// games.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// cost is the bcrypt cost of new hashes. It is a variable so tests can lower
// it.
var cost = bcrypt.DefaultCost

// ErrPasswordNotSet is returned when no Iris password has been set yet.
var ErrPasswordNotSet = errors.New("iris password is not set")

// CheckPassword returns nil if password is the Iris password. It returns
// bcrypt.ErrMismatchedHashAndPassword if it isn't.
func CheckPassword(ctx context.Context, db *sql.DB, password string) error {
	var hash string
	err := db.QueryRowContext(ctx, "SELECT password_hash FROM iris_auth WHERE id=1").Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPasswordNotSet
	} else if err != nil {
		return err
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// SetPassword sets the Iris password, replacing it if it is already set.
func SetPassword(ctx context.Context, db *sql.DB, password string) error {
	if password == "" {
		return errors.New("iris password cannot be empty")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return fmt.Errorf("iris password: %w", err)
	}
	_, err = db.ExecContext(ctx,
		"INSERT INTO iris_auth (id, password_hash) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET password_hash=excluded.password_hash",
		hash,
	)
	return err
}

// PasswordIsSet reports whether an Iris password has been set.
func PasswordIsSet(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM iris_auth").Scan(&n)
	return n > 0, err
}
