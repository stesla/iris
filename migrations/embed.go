// Package migrations holds the goose migrations for the Iris database, so
// tests can apply them.
package migrations

import "embed"

// FS holds every migration.
//
//go:embed *.sql
var FS embed.FS
