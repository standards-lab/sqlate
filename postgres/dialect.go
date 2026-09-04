package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/standards-lab/sqlate"
)

// Dialect is the PostgreSQL dialect: sqlate.Dialect plus the sqlate.Locker
// capability. It carries no state; the zero value is the dialect.
type Dialect struct{}

var (
	_ sqlate.Dialect = Dialect{}
	_ sqlate.Locker  = Dialect{}
)

// Name identifies the engine.
func (Dialect) Name() string { return "postgres" }

// Placeholder renders the 1-based nth bind parameter as $n.
func (Dialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }

// MapError classifies a driver error by its SQLSTATE: class 22 as
// sqlate.ErrInvalidValue with the engine's message reachable, the four
// class-23 constraint violations as a sqlate.ConstraintError carrying the
// constraint name, and everything else unchanged. nil stays nil.
func (Dialect) MapError(err error) error {
	if err == nil {
		return nil
	}
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok {
		return err
	}
	if strings.HasPrefix(pgErr.Code, "22") {
		return fmt.Errorf("%w: %w", sqlate.ErrInvalidValue, err)
	}
	var class error
	switch pgErr.Code {
	case "23505":
		class = sqlate.ErrUniqueViolation
	case "23503":
		class = sqlate.ErrForeignKeyViolation
	case "23514":
		class = sqlate.ErrCheckViolation
	case "23502":
		class = sqlate.ErrNotNullViolation
	default:
		return err
	}
	return &sqlate.ConstraintError{Constraint: pgErr.ConstraintName, Class: class, Err: err}
}

// Lock takes the session-level advisory lock for name on conn, blocking
// until it is granted or ctx ends. The name enters the engine's 32-bit key
// space through hashtext. The lock belongs to the connection's session and
// outlives any transaction on it; Unlock on the same conn releases it.
func (Dialect) Lock(ctx context.Context, conn *sql.Conn, name string) error {
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext($1))", name); err != nil {
		return fmt.Errorf("advisory lock %s: %w", name, err)
	}
	return nil
}

// Unlock releases the session-level advisory lock for name on conn.
func (Dialect) Unlock(ctx context.Context, conn *sql.Conn, name string) error {
	var released bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock(hashtext($1))", name).Scan(&released); err != nil {
		return fmt.Errorf("advisory unlock %s: %w", name, err)
	}
	if !released {
		return fmt.Errorf("%w: %s", ErrLockNotHeld, name)
	}
	return nil
}
