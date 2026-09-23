package sqltest

import (
	"strings"

	"github.com/standards-lab/sqlate/query"
)

// ReturningDialect is the stub dialect with the query.Returner capability:
// it appends "RETURNING <columns>" to an INSERT or UPDATE, as an engine with
// the clause renders it, and declines any other verb. It lets a consumer's
// unit suite cover a returning command's single-statement form, where
// Dialect covers the fallback.
type ReturningDialect struct{ Dialect }

var _ query.Returner = ReturningDialect{}

// Returning appends the RETURNING clause to body for query.Insert and
// query.Update and declines every other verb.
func (ReturningDialect) Returning(verb query.Verb, body string, columns []string) (string, bool) {
	switch verb {
	case query.Insert, query.Update:
		return body + "\nRETURNING " + strings.Join(columns, ", "), true
	}
	return "", false
}
