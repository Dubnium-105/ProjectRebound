// Package migrations exposes the versioned PostgreSQL migrations to the
// control-plane binary. Embedding keeps production images self-contained.
package migrations

import "embed"

// Files contains every forward migration in version order.
//
//go:embed *.sql
var Files embed.FS
