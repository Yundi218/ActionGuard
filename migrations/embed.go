package migrations

import "embed"

// FS contains the ordered public database migrations.
//
//go:embed *.sql
var FS embed.FS
