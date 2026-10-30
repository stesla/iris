// Package database opens the Iris database.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	_ "github.com/mattn/go-sqlite3"
)

// Open opens the SQLite database at path. It fails if the file doesn't exist,
// because sql.Open would silently create an empty one, or if the migrations
// haven't been run.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if err := requireMigrated(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// requireMigrated fails if the database doesn't have the iris_auth table,
// which the most recent migration adds.
func requireMigrated(ctx context.Context, db *sql.DB) error {
	var n int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='iris_auth'",
	).Scan(&n)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	if n == 0 {
		return errors.New("database has no iris_auth table (have the migrations been run?)")
	}
	return nil
}
