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
	"github.com/standards-lab/sqlate/migrate"
	"github.com/standards-lab/sqlate/query"
)

// Dialect is the PostgreSQL dialect: sqlate.Dialect plus the sqlate.Locker
// and query.Returner capabilities and the server-version statement. It has
// no fields; the zero value is the dialect.
type Dialect struct{}

var (
	_ sqlate.Dialect  = Dialect{}
	_ sqlate.Locker   = Dialect{}
	_ migrate.Catalog = Dialect{}
	_ query.Returner  = Dialect{}
)

// Name identifies the engine.
func (Dialect) Name() string { return "postgres" }

// Placeholder renders the 1-based nth bind parameter as $n.
func (Dialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }

// MapError classifies a driver error by its SQLSTATE: class 22 as
// sqlate.ErrInvalidValue with the engine's message reachable, 2BP01 as
// sqlate.ErrDependentObjects, 40001 as sqlate.ErrSerializationFailure, the
// four class-23 constraint violations as a sqlate.ConstraintError with the
// constraint, table, and column names the server reports, and everything
// else unchanged. nil stays nil.
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
	switch pgErr.Code {
	case "2BP01":
		return fmt.Errorf("%w: %w", sqlate.ErrDependentObjects, err)
	case "40001":
		return fmt.Errorf("%w: %w", sqlate.ErrSerializationFailure, err)
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
	return &sqlate.ConstraintError{
		Constraint: pgErr.ConstraintName,
		Table:      pgErr.TableName,
		Column:     pgErr.ColumnName,
		Class:      class,
		Err:        err,
	}
}

// ServerVersion returns the statement an administrative read runs to learn
// the server's version: one row, one text column. Every engine spells it
// differently, so the dialect carries it as a capability an administrative
// layer discovers by type assertion.
func (Dialect) ServerVersion() string { return "SELECT version()" }

// CreateHistory returns migrate.StandardCatalog's DDL unchanged: its CREATE
// TABLE IF NOT EXISTS with text and boolean columns already suits the engine.
func (Dialect) CreateHistory(table string) string {
	return migrate.StandardCatalog{}.CreateHistory(table)
}

// HistoryExists returns the existence check restricted to the session's
// current schema. migrate.StandardCatalog's form matches table_name in every
// schema information_schema.tables lists, so a same-named table in another
// schema satisfies it while the current schema's history table is missing.
// current_schema() is a PostgreSQL function, so the fix lives here rather
// than in StandardCatalog, whose other engines spell it differently.
func (Dialect) HistoryExists(param string) string {
	return "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = " + param
}

// Returning renders the single-statement form of a returning command for
// query.Insert and query.Update: the body with RETURNING and the read's
// columns appended, so the command itself returns the changed row. The
// clause starts on a new line, so a line comment ending the body does not
// swallow it. Returning declines every other verb.
func (Dialect) Returning(verb query.Verb, body string, columns []string) (string, bool) {
	switch verb {
	case query.Insert, query.Update:
		return body + "\nRETURNING " + strings.Join(columns, ", "), true
	}
	return "", false
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
