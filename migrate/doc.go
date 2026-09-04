// Package migrate is schema versioning over authored SQL: an ordered set of
// migrations, each SQL text plus a version, applied against a history table
// on one pinned connection under the dialect's lock capability. It designs
// for the cases a mature library has met: dirty state after a failed
// non-transactional migration (recorded, reported, cleared only by Force),
// engines without transactional DDL (a migration headed "-- transaction:
// none" runs outside a transaction and contains exactly one statement by convention),
// and concurrent starters (the lock; a dialect without one fails unless the
// consumer opts into an unlocked run). Files packages the
// NNNN_name.{up,down}.sql layout as a helper, never the contract.
package migrate
