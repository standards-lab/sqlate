package sqlate

import (
	"context"
	"database/sql"
	"fmt"
)

// Dialect is what the library needs from an engine: its name, how it renders
// the nth bind parameter, and how its driver's errors classify. An engine
// sub-module implements it; a provider library's dialect satisfies it
// structurally. Capabilities beyond it, Locker and ErrorMapper, are separate
// interfaces a protocol asserts.
type Dialect interface {
	// Name identifies the engine.
	Name() string
	// Placeholder renders the 1-based nth bind parameter ("$1" for
	// postgres, "@p1" for a future mssql).
	Placeholder(n int) string
	// MapError translates a driver error into the library's sentinels;
	// it returns sql.ErrNoRows unchanged.
	MapError(err error) error
}

// Session is the stdlib method set a runner needs, implemented by *DB and
// *Tx, so the same statement handle runs against the pool or inside a
// transaction. Every method maps driver errors through the dialect; the
// dialect itself is not on the interface, because runners never need it at
// request time.
type Session interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// ErrorMapper is the capability both sessions expose for errors that arise
// after a call returns, from rows.Err and Scan, which the session's methods
// cannot see. A runner type-asserts it on its Session.
type ErrorMapper interface {
	MapError(err error) error
}

// Locker is the dialect capability a concurrent-starter protocol takes: a
// session-scoped exclusive lock on a dedicated connection, identified by
// name. Locks are named, never numbered (<owner>.<structure>, such as
// migrate.schema_version or organization.tree), and the dialect maps the
// name to its engine's lock space. A provider without the capability cannot
// serialize migrations across processes.
type Locker interface {
	Lock(ctx context.Context, conn *sql.Conn, name string) error
	Unlock(ctx context.Context, conn *sql.Conn, name string) error
}

// DB is the pool session over a plain *sql.DB. Lifecycle (opening,
// readiness, closing) belongs to whoever owns the pool; DB adds mapping,
// prepare, options on Begin, and pinned connections.
type DB struct {
	pool    *sql.DB
	dialect Dialect
}

var (
	_ Session     = (*DB)(nil)
	_ Session     = (*Tx)(nil)
	_ ErrorMapper = (*DB)(nil)
	_ ErrorMapper = (*Tx)(nil)
)

// Wrap builds the session over pool with dialect, the engine's or a
// capability-adding wrapper of it. Nil arguments are a defect in the
// caller and panic.
func Wrap(pool *sql.DB, dialect Dialect) *DB {
	if pool == nil || dialect == nil {
		panic("sqlate: Wrap requires a pool and a dialect")
	}
	return &DB{pool: pool, dialect: dialect}
}

// Dialect returns the dialect statements are compiled against.
func (d *DB) Dialect() Dialect { return d.dialect }

// MapError routes err through the dialect; nil stays nil.
func (d *DB) MapError(err error) error {
	if err == nil {
		return nil
	}
	return d.dialect.MapError(err)
}

// Conn pins one connection for a protocol that needs session scope: a
// session-level lock, a run of non-transactional DDL. The caller closes it.
func (d *DB) Conn(ctx context.Context) (*sql.Conn, error) {
	conn, err := d.pool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	return conn, nil
}

func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := d.pool.ExecContext(ctx, query, args...)
	return res, d.MapError(err)
}

func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := d.pool.QueryContext(ctx, query, args...)
	return rows, d.MapError(err)
}

func (d *DB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	st, err := d.pool.PrepareContext(ctx, query)
	return st, d.MapError(err)
}
