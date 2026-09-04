// Package postgres is the PostgreSQL dialect of sqlate: how the engine
// renders the nth bind parameter, how its driver's errors classify, and the
// session-level advisory lock the migrate protocol takes. It is a
// sub-module so the driver it names, pgx, enters a consumer's build only
// through this import, made once at the composition root; a consumer opens
// its own pool with the driver and hands it to sqlate.Wrap with [Dialect].
//
// # Error classification
//
// [Dialect.MapError] reads the SQLSTATE the driver exposes. Class 22, a
// data exception (invalid text for a type, a value out of range, a bad
// datetime), becomes sqlate.ErrInvalidValue wrapping the driver error, the
// engine-side half of request validation. Class 23 constraint violations
// become a sqlate.ConstraintError whose fields are the class sentinel, the
// driver error, and the violated constraint's name: 23505 unique, 23503 foreign key, 23514 check,
// 23502 not null. MapError returns everything else unchanged, sql.ErrNoRows
// included, and errors.As finds the driver error through every wrap.
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
// # Native forms
//
// The sqlint.toml beside this file exports the engine's native forms: the
// spellings a standard-tier SQL file must not use, each a regular
// expression under the name a finding reports. PostgreSQL is the standard
// spelling of every library pattern, so the module ships no overlay.
package postgres
