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
// protocol's own conflict, with the expected and current versions in its
// text. A service maps it to 412.
var ErrVersionMismatch = errors.New("version mismatch")

// ErrRefused classifies a guarded command whose row exists, at the version
// the caller expected, but whose own predicate — beyond the key and the
// version — still matched no row. A plain Guard's command carries no such
// predicate, so its own equal-version branch can never actually be reached:
// a row at the expected version would already have matched the command's
// update. RowGuard's second predicate is what makes this a real, reachable
// outcome.
var ErrRefused = errors.New("refused")

// RefusedError is a guarded command's row-level refusal: the row
// RowGuard read back, at the version the caller expected. Unwrap yields
// ErrRefused.
type RefusedError[T any] struct {
	Version int64
	Row     T
}

func (e *RefusedError[T]) Error() string {
	return fmt.Sprintf("%v: version %d unchanged", ErrRefused, e.Version)
}

func (e *RefusedError[T]) Unwrap() error { return ErrRefused }

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
// with the expected and current versions.
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
	// Unreachable for a plain Guard, whose command matches on the key and
	// the version alone: a row at the expected version would have matched
	// the update. Kept for shape-consistency with RowGuard.Run.
	if current == version {
		return 0, fmt.Errorf("%w: version %d unchanged", ErrRefused, version)
	}
	return 0, fmt.Errorf("%w: expected %d, current %d", ErrVersionMismatch, version, current)
}

// RowGuard is Guard's counterpart over a returning command whose own
// predicate, beyond the key and the version, may refuse the write. It
// returns the row as it stands afterward. When the command changed nothing,
// it uses the row the returning handle already read back to tell apart no
// row at all, a row at another version, and a row at the expected version
// that the command's own predicate refused. Returning.Guarded builds it.
type RowGuard[T any] struct {
	returning Returning[T]
	version   string
	current   func(T) int64
}

// Run runs the command through its handle's One, with version bound under
// the guard's parameter name alongside args. A changed row is success: Run
// returns the row as it stands afterward, whose new version is
// current(row). An unchanged row is classified: no row is sql.ErrNoRows; a
// row whose current version does not match the expected one is
// ErrVersionMismatch, with both versions in its text; a row at the expected
// version is a *RefusedError[T] carrying it, since the command's own
// predicate refused the write. Every error One returns, ErrNotOneRow among
// them, passes through.
func (g RowGuard[T]) Run(ctx context.Context, s sqlate.Session, version int64, args Args) (T, error) {
	var zero T
	bound := make(Args, len(args)+1)
	maps.Copy(bound, args)
	bound[g.version] = version
	row, changed, err := g.returning.One(ctx, s, bound)
	if err != nil {
		return zero, err
	}
	if changed {
		return row, nil
	}
	current := g.current(row)
	if current == version {
		return zero, &RefusedError[T]{Version: version, Row: row}
	}
	return zero, fmt.Errorf("%w: expected %d, current %d", ErrVersionMismatch, version, current)
}
