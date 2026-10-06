// Package migrate is schema versioning over authored SQL: ordered sets of
// migrations, each migration SQL text plus a version, applied against a
// history table on one pinned connection under the dialect's lock
// capability. It designs for the cases a mature library has met: dirty
// state after a failed non-transactional migration (recorded, reported,
// cleared only by Force), engines without transactional DDL (a migration
// headed "--| transaction: none" runs outside a transaction and contains
// exactly one statement by convention), and concurrent starters (the lock;
// a dialect without one fails unless the consumer opts into an unlocked
// run). The NNNN_name.{up,down}.sql file layout is a helper, never the
// contract.
//
// A program installed under an earlier single-set migrator adopts sets
// without migrating its history: the default table and the history's
// columns are the same.
//
// Force is the operator repair for a dirty set. The repair is: fix the
// failed migration's objects by hand, Force to the version that is
// applied, then Up.
//
// The package exports:
//
//   - [Migrator], which runs one or more sets, built by [New] with its
//     [Options]
//   - [Set], one migration layer, with [DefaultTable], the history table a
//     set without one uses
//   - [Migration], one schema step, and [Files], which reads the file
//     layout into a set's migrations
//   - [Layer], a handle on one of a migrator's sets
//   - [Version], a history's head, and [SetStatus], one set's state
//   - [Catalog], the engine-specific half of the history protocol, and
//     [StandardCatalog], the one a dialect without its own gets
//   - [SetError], an error naming its set, over the details [DirtyError],
//     [PendingError], and [UnknownVersionError] and their classes
//     [ErrDirty], [ErrPending], and [ErrUnknownVersion]
//   - [ErrNoLocker], [ErrNoDown], [ErrVersionNotFound], [ErrAboveApplied],
//     and [ErrBelowPending], the refusals of a run, a revert, a force, and
//     the layer ordering
package migrate
