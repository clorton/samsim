// Package migrations embeds the aggregator's ordered SQL migration files.
package migrations

import "embed"

// FS contains the ordered *.sql migration files in this directory.
//
//go:embed *.sql
var FS embed.FS
