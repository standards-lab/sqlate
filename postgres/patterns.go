package postgres

import (
	"embed"

	"github.com/standards-lab/sqlate/query"
)

//go:embed overlay/*.sql
var overlayFiles embed.FS

// Patterns returns the library's patterns, query.Patterns, with the keyset
// predicate respelled as the engine's row-value comparison in place of the
// standard tier's expanded chain of disjuncts. A program passes it to
// query.NewCatalog in place of query.Patterns; every other pattern is the
// library's own.
func Patterns() query.Source {
	return query.Patterns().Overlay(overlayFiles, "overlay")
}
