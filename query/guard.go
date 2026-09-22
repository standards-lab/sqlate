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
// predicate, so an equal-version check result there means the row was
// concurrently restored to the expected version between the command and
// the check, not a refusal; RowGuard's second predicate is what makes this
// a real, reachable outcome.
var ErrRefused = errors.New("refused")

// RefusedError is a guarded command's row-level refusal: the row
// RowGuard's check read, at the version the caller expected. Unwrap
// yields ErrRefused.
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

// RowGuard is Guard's counterpart for a command with its own second
// predicate: check reads the whole row instead of a bare version, so Run
// can tell no row at all, a row at another version, and a row at the
// expected version the command's own predicate refused apart, where Guard
// could only ever report the last two cases as the same version mismatch.
type RowGuard[T any] struct {
	command Statement
	check   Rows[T]
	version string
	current func(T) int64
}

// Run executes the command with version bound under the guard's parameter
// name alongside args, exactly as Guard.Run. A row affected is success and
// the new version, version+1, with no second round trip. No row affected
// reads the row with the check, using the same bound args: no row is
// sql.ErrNoRows; a row whose current version (read through the current
// function) does not match the expected one is ErrVersionMismatch, with
// both versions in its text; a row at the expected version is a
// *RefusedError[T] carrying it, since the command's own predicate is what
// refused the write.
func (g RowGuard[T]) Run(ctx context.Context, s sqlate.Session, version int64, args Args) (int64, error) {
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
	row, err := g.check.One(ctx, s, bound)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, sql.ErrNoRows
	}
	if err != nil {
		return 0, err
	}
	current := g.current(row)
	if current == version {
		return 0, &RefusedError[T]{Version: version, Row: row}
	}
	return 0, fmt.Errorf("%w: expected %d, current %d", ErrVersionMismatch, version, current)
}
