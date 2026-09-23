# Changelog

All notable changes to the PostgreSQL dialect (`github.com/standards-lab/sqlate/postgres`) are
documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the module adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This
changelog covers this sub-module only; the base module keeps its own.

## [v0.4.0] - 2026-09-23

### Changed

- The live proofs cover the total counted in the page's own statement. A page agrees with its
  total while writes commit between the statement and the caller, where a count read as its own
  statement does not. It agrees under concurrent writers on the pool. The counted page plans one
  window over one scan of its base, while the uncounted page reads the key's index in order.
  The page-past-the-end assertion expects `NoTotal`, and the cursor proof checks that each
  continued page carries both the row-value predicate and the window count.

Requires `github.com/standards-lab/sqlate v0.4.0`.

## [v0.3.0] - 2026-09-23

### Added

- `Dialect.Returning`, implementing `query.Returner`: a returning `INSERT` or `UPDATE` compiles
  with `RETURNING` and its read's columns appended, so the command itself returns the changed
  row in one statement. The method declines every other verb.

### Changed

- The live row-guard proof runs on the returning handle, `Returning(scan).Guarded(version,
  current)`, in place of the removed `GuardedRow`, and covers both forms of the command. A new
  live proof shows that both forms return the same row in every outcome.

Requires `github.com/standards-lab/sqlate v0.3.0`.

## [v0.2.0] - 2026-09-22

### Added

- `Dialect.CreateHistory` and `HistoryExists`, implementing `migrate.Catalog`. `HistoryExists`
  is qualified by the session's current schema, closing a defect in `migrate.StandardCatalog`'s
  own form: a same-named history table in an unrelated schema satisfied the check even though
  the current schema's own table did not exist.
- `Patterns()`, the library's own patterns with the keyset predicate overlaid as the engine's
  row-value comparison in place of the standard tier's expanded chain of disjuncts.
- `MapError` fills `ConstraintError.Table` and `.Column` from the driver, and maps SQLSTATE
  `2BP01` (dependent objects still exist) and `40001` (serialization failure) to
  `sqlate.ErrDependentObjects` and `sqlate.ErrSerializationFailure`.

Requires `github.com/standards-lab/sqlate v0.2.0`.

## [v0.1.1] - 2026-09-04

### Added

- `Dialect.ServerVersion`, the statement an administrative layer runs to read the engine's
  version (`SELECT version()`), carried as a capability beside `Lock` and `Unlock`.

## [v0.1.0] - 2026-09-04

The first release of the PostgreSQL dialect, against `github.com/standards-lab/sqlate v0.1.0`.

### Added

- `Dialect`: `$n` placeholders; `MapError` over pgx's error, SQLSTATE class 22 to
  `sqlate.ErrInvalidValue` and the four class-23 constraint violations to a
  `sqlate.ConstraintError` with the constraint name; `Lock` and `Unlock` over session-level
  advisory locks, with `ErrLockNotHeld`.
- `sqlint.toml` exporting the engine's native forms.
- The integration tier: the migrate and query proofs against a live engine, behind the
  `integration` build tag.

[Unreleased]: https://github.com/standards-lab/sqlate/compare/postgres/v0.2.0...HEAD
[v0.2.0]: https://github.com/standards-lab/sqlate/compare/postgres/v0.1.1...postgres/v0.2.0
[v0.1.1]: https://github.com/standards-lab/sqlate/releases/tag/postgres/v0.1.1
[v0.1.0]: https://github.com/standards-lab/sqlate/releases/tag/postgres/v0.1.0
