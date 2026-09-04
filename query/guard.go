package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"

	"github.com/standards-lab/sqlate"
)

// ErrVersionMismatch classifies a guarded command whose row exists at
// another version than the one the caller read: the optimistic-concurrency
// protocol's own conflict, carrying the expected and current versions in
// its text. A service maps it to 412.
var ErrVersionMismatch = errors.New("version mismatch")

// Guard is the optimistic-concurrency protocol over two authored
// statements: the command, whose WHERE names the key and the expected
// version and whose SET advances it, and the check, which reads the row's
// current version by key. The SQL is the consumer's; the guard is the
// function over it.
type Guard struct {
	command Statement
	check   Statement
	version string
}

// Run executes the command with version bound under the guard's parameter
// name alongside args. A row affected is success and the new version,
// version+1, with no second round trip. No row affected runs the check with
// the same args: no row is sql.ErrNoRows, a row is ErrVersionMismatch
// carrying the expected and current versions.
func (g Guard) Run(ctx context.Context, s sqlate.Session, version int64, args Args) (int64, error) {
	bound := make(Args, len(args)+1)
	maps.Copy(bound, args)
	bound[g.version] = version
	n, err := g.command.Exec(ctx, s, bound)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		return version + 1, nil
	}
	current, err := g.check.Scan(Scalar[int64]).One(ctx, s, bound)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, sql.ErrNoRows
	}
	if err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("%w: expected %d, current %d", ErrVersionMismatch, version, current)
}
