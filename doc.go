// Package sqlate is the session layer of the sqlate library, which every
// authored statement runs through. It wraps a plain *sql.DB behind the
// stdlib method set (ExecContext, QueryContext, PrepareContext), maps every
// driver error through the engine's dialect at that boundary, and owns the
// transaction runner. The package depends on the standard library alone;
// each engine lives in a sub-module (postgres) that supplies the dialect, so
// a consumer imports its engine once, where it opens its pool. The packages
// above it, query and migrate, take a Session and never a driver.
//
// # Wrapper
//
// [Wrap] builds the [DB] session over a pool with the engine's [Dialect].
// Lifecycle (opening, readiness, closing) belongs to whoever owns the
// pool; DB adds error mapping, prepare, options on Begin, and pinned
// connections. It performs no I/O: a nil pool or dialect is a defect in
// the caller and panics. [DB.Conn] pins one connection for a
// protocol that needs session scope, such as a session-level lock or a run
// of non-transactional DDL; [DB.Dialect] returns the dialect statements are
// compiled against.
//
// # Sessions and transactions
//
// [Session] is the method set the query and migrate packages take,
// implemented by both [DB] and [Tx], so the same statement runs against the
// pool or inside a transaction. The consumer owns the transaction boundary:
// [DB.Begin] opens a [Tx] with [TxOption] values applied, and [DB.Transact]
// runs one unit of work with a result: commit on success, rollback on the
// unit's error or panic. [Tx.Commit] routes its error through the dialect's
// MapError, the one place a violation deferred to COMMIT can be classified.
// [ErrorMapper] is the capability both sessions expose for errors that arise
// after a call returns, rows.Err and Scan, which the session cannot see.
//
// # Dialect
//
// [Dialect] is what the library needs from an engine: its name, how it
// renders the nth bind parameter, and how its driver's errors classify. An
// engine sub-module implements it. Capabilities beyond it are separate
// interfaces a protocol asserts: [Locker], the named session-scoped lock a
// concurrent-starter protocol such as migration takes.
//
// # Errors
//
// The package owns the error taxonomy consumers match on.
// [ErrConnectionFailed] classifies a connectivity failure and
// [ErrInvalidValue] a data exception, each wrapped in the dual form
// fmt.Errorf("%w: %w", sentinel, err) so errors.Is classifies while the
// driver's error stays recoverable. The session classifies connectivity
// itself, before the dialect: a network error or driver.ErrBadConn from any
// call means the engine never saw the statement, so the dialect's
// vocabulary does not apply. A dialect's MapError returns the four
// constraint classes ([ErrUniqueViolation], [ErrForeignKeyViolation],
// [ErrCheckViolation], [ErrNotNullViolation]) inside a [ConstraintError],
// with the constraint name when the driver exposes it. sql.ErrNoRows is
// never mapped; MapError returns it unchanged.
package sqlate
