// Package migrate is schema versioning over authored SQL: ordered sets of
// migrations, each migration SQL text plus a version, applied against a
// history table on one pinned connection under the dialect's lock
// capability. It designs for the cases a mature library has met: dirty
// state after a failed non-transactional migration (recorded, reported,
// cleared only by Force), engines without transactional DDL (a migration
// headed "-- transaction: none" runs outside a transaction and contains
// exactly one statement by convention), and concurrent starters (the lock;
// a dialect without one fails unless the consumer opts into an unlocked
// run). Files packages the NNNN_name.{up,down}.sql layout as a helper,
// never the contract.
//
// # Sets
//
// A Set is one migration layer as a library ships it or a program declares
// it: a name, the history table its migrations are recorded in, and its
// migrations in version order. A Migrator runs one or more sets, declared
// bottom-first, each over its own history table: a set's migrations may
// reference the objects of the sets declared before it, never the objects
// of the sets declared after it. A set that leaves Set.Table empty uses
// DefaultTable, so at most one set per migrator may leave it empty. A
// program installed under an earlier single-set migrator adopts sets
// without migrating its history: the default table and the history's
// columns are the same.
//
// # The run model
//
// Every run that writes takes one lock, pins one connection, and creates
// and reads every set's history table before any set's first migration
// runs. A dirty row or a history that does not match its set refuses the
// run there, naming the set, with nothing run against the schema. The read
// happens inside the lock, so no starter can change a history between the
// check and the run. Options.Unlocked skips the lock; its intended shape
// is a caller that holds a lock of its own around every run, and
// concurrent starters without one are unsafe.
//
// # Verbs
//
// A Migrator method that names no set acts on the top set, the last one
// declared: Version, Migrations, Steps, Down, and Force. Up, Verify,
// Reset, and Status cover every set, in declared order, and Reset reverts
// in reverse declared order and drops each set's history table once that
// set is reverted.
//
// Layer is a handle on one named set, from Migrator.Layers or
// Migrator.Layer, and its verbs act on that set alone under the migrator's
// ordering rules: a revert is ErrAboveApplied while a layer above still
// has applied migrations, and an apply is ErrBelowPending while a layer
// below still has pending ones. Reverting more migrations than a set has
// applied is not an error, so Down with the set's whole length reverts
// everything it has applied.
//
// Force is the operator repair for a dirty set. It sets a set's history to
// a version without running anything against the schema, and it is the one
// verb that checks no set's history first, since the set it repairs is the
// dirty one. The repair is: fix the failed migration's objects by hand,
// Force to the version that is applied, then Up.
package migrate
