// Package migrations embeds the database migration SQL files.
// The embed is placed here so the path to migrations/ is valid relative to this package.
package migrations

import "embed"

// FS holds the embedded migration files.
//
//go:embed sql/*.sql
var FS embed.FS
