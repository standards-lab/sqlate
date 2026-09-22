// Package postgres is the PostgreSQL dialect of sqlate:
//
//   - how the engine renders the nth bind parameter
//   - how its driver's errors classify
//   - the session-level advisory lock the migrate protocol takes
//   - the statement that reads the server's version
//   - its spelling of the keyset predicate a cursor continues by
//   - its migrate.Catalog implementation, the history protocol's engine half
//
// It is a sub-module so the driver it names, pgx, enters a consumer's build only
// through this import, made once where it opens its pool; a consumer opens
// its own pool with the driver and hands it to sqlate.Wrap with [Dialect].
//
// # Error classification
//
// [Dialect.MapError] reads the SQLSTATE the driver exposes. Class 22, a
// data exception (invalid text for a type, a value out of range, a bad
// datetime), becomes sqlate.ErrInvalidValue wrapping the driver error, the
// engine-side half of request validation. Class 23 constraint violations
// become a sqlate.ConstraintError whose fields are the class sentinel, the
// driver error, and the violated constraint's name, table, and column when
// the driver exposes them — a not-null violation names no constraint, so
// the column is the only handle a consumer has for that case:
//
//   - 23505 unique
//   - 23503 foreign key
//   - 23514 check
//   - 23502 not null
//
// 2BP01 (dependent objects still exist) and 40001 (serialization failure)
// become sqlate.ErrDependentObjects and sqlate.ErrSerializationFailure, the
// two classes a multi-set migrator's own tests need. MapError returns
// everything else unchanged, sql.ErrNoRows included, and errors.As finds
// the driver error through every wrap.
//
// # Locking
//
// [Dialect.Lock] and [Dialect.Unlock] implement sqlate.Locker over
// pg_advisory_lock and pg_advisory_unlock on a pinned connection. The name
// enters the engine's key space through hashtext, so locks are named and
// never numbered; the lock belongs to the connection's session and outlives
// any transaction on it. Unlock of a lock the session does not hold is
// [ErrLockNotHeld].
//
// # Server version
//
// [Dialect.ServerVersion] returns SELECT version(), the statement an
// administrative layer runs to report the engine's version. It is a
// capability rather than part of sqlate.Dialect, since every engine spells
// the read differently and the session layer never needs it.
//
// # Migration history
//
// [Dialect.CreateHistory] delegates to migrate.StandardCatalog unchanged.
// [Dialect.HistoryExists] qualifies the check by the session's current
// schema, closing a defect in the standard form: information_schema.tables
// spans every schema on the search path, so a same-named table in an
// unrelated schema would satisfy the check even though the current
// schema's own history table does not exist.
//
// # Native forms
//
// The sqlint.toml beside this file exports the engine's native forms: the
// spellings a standard-tier SQL file must not use, each a regular
// expression under the name a finding reports. The same file exports the
// module's overlay directory to the lint.
//
// # Patterns
//
// [Patterns] returns the library's patterns with one overlaid: the keyset
// predicate a cursor continues by. The library composes it in standard SQL
// as an expanded chain of disjuncts, (a > x) OR (a = x AND b > y); the
// overlay spells it as the row-value comparison (a, b) > (x, y), which the
// engine evaluates as one condition over the keyed columns. Every other
// pattern is accepted as written, so a program that composes [Patterns] in
// place of query.Patterns changes only the text of a continued page's
// predicate.
package postgres
