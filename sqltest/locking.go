package sqltest

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/standards-lab/sqlate"
)

// LockingDialect is the stub dialect with the sqlate.TryLocker capability,
// and so sqlate.Locker: each call runs one statement on the pinned
// connection, so the lock calls are part of the recorded script and a test
// scripts their outcomes like any other call.
//
//   - Lock executes "SELECT lock($1)" with the name; script an exec
//     response, or an error for a lock that fails.
//   - TryLock queries "SELECT try_lock($1)" with the name and reports the
//     one boolean row; script true for a granted lock, false for one
//     another session holds.
//   - Unlock queries "SELECT unlock($1)" with the name; script true for a
//     held lock, false for one that was not, which is ErrLockNotHeld.
//
// It lets a consumer's unit suite cover a protocol that takes a lock, where
// Dialect covers a provider without the capability.
type LockingDialect struct{ Dialect }

var _ sqlate.TryLocker = LockingDialect{}

// Lock executes the scripted lock statement for name on conn.
func (LockingDialect) Lock(ctx context.Context, conn *sql.Conn, name string) error {
	_, err := conn.ExecContext(ctx, "SELECT lock($1)", name)
	return err
}

// TryLock queries the scripted try-lock statement for name on conn and
// reports its boolean.
func (LockingDialect) TryLock(ctx context.Context, conn *sql.Conn, name string) (bool, error) {
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT try_lock($1)", name).Scan(&got); err != nil {
		return false, err
	}
	return got, nil
}

// Unlock queries the scripted unlock statement for name on conn; a false
// row is ErrLockNotHeld.
func (LockingDialect) Unlock(ctx context.Context, conn *sql.Conn, name string) error {
	var held bool
	if err := conn.QueryRowContext(ctx, "SELECT unlock($1)", name).Scan(&held); err != nil {
		return err
	}
	if !held {
		return fmt.Errorf("%w: %s", ErrLockNotHeld, name)
	}
	return nil
}
