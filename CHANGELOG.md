# Changelog

All notable changes to `github.com/standards-lab/sqlate` are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). This changelog covers the base
module only; the `postgres` and `sqlint` sub-modules each keep their own.

## [Unreleased]

The returning command, promoted from the `blobfs` experiment, whose engine kept two copies of
four statements for want of it. A command that returns its changed row is declared once and runs
with `RETURNING` on an engine that has the clause, or as the command and then its read in one
transaction on an engine that does not.

### Added

- The `returning` header key: a standard-tier `INSERT INTO` or `UPDATE` names the statement in
  its directory that reads the changed row back. `Compile` resolves the pair and refuses an
  invalid one, including a read of a table other than the one the command changes.
- `query.Returner` and `query.Verb`: the dialect capability that renders the single-statement
  form at compile time. A command whose dialect lacks it, or declines, runs the fallback.
- `Statement.Returning(scan)`, returning a `query.Returning[T]` whose `One` returns the row as it
  stands afterward and whether the command changed it. `Statement.Reads` and
  `Statement.ReturningText` report the read's name and the single-statement form.
- `query.ErrNotOneRow`: a command that changed more than one row, or whose read does not find
  the row it changed.
- `sqlate.Beginner`, the session capability to open a transaction, satisfied by `*DB` and any
  type embedding it.
- `sqlate.Transact(ctx, b, fn, opts...)`, the unit-of-work runner over any `Beginner`, with
  `DB.Transact`'s semantics; `DB.Transact` now delegates to it.
- `sqltest.ReturningDialect`, a stub dialect with `query.Returner`, so a unit suite covers the
  single-statement form.
- `Statements.Verify` also prepares each returning command's single-statement form.

### Changed

- **Breaking:** `Returning(scan).Guarded(version, current)` builds `RowGuard[T]`, and its `Run`
  returns the changed row instead of the new version. `Statement.GuardedRow` is removed.

## [v0.2.0] - 2026-09-22

The multi-set migrator promoted from the `blobfs` experiment, and six adjustments that
strengthen `sqlate` as a library other packages ship their own schema and patterns over.

### Added

- `migrate.Set`: a `Migrator` now runs one or more sets, each over its own history table, in
  declared order, under one lock on one pinned connection, with every set's history checked
  before any set runs. New: `Migrator.Reset`, `Status`, `Layers`/`Layer` (a named-set handle
  carrying its own ordering refusals, `ErrAboveApplied`/`ErrBelowPending`), and `*SetError`,
  naming the set an error came from. Every method a single-set `Migrator` already had keeps its
  exact signature and now acts on the top (last-declared) set.
- `Projection.Continue`, the cursor-paging counterpart to `List`: a request continues from a
  previous page's `Collection.Next` instead of an offset, composed as a keyset predicate (the
  standard tier's expanded `OR`-chain, or an engine's row-value overlay). `List` and `Continue`
  both return `Collection[T]` (`Items`, `Total`, `More`, `Next`); `Directives.Total` selects
  `TotalExact` (the existing count) or the new `TotalNone`, which skips it.
- `query.With` and a projection base binding its own parameters: `List`, `Continue`, and `One`
  take `base ...Args`, merged left to right, so a base statement may reference a value the
  program supplies rather than the request.
- A composite `--| key: a, b`; `Statement.Keys() []string` for its parts (`Key()` still returns
  them comma-joined, as it always has for a single key).
- `--| field: ... not null` (`Field.NotNull`), matched case-insensitively, marking a field
  eligible for a cursor's keyed ordering.
- An optional `--| port:` declaration, and `--| [key]: more` header continuation, folding a long
  declaration's value across lines.
- `query.RowGuard[T]` and `Statement.GuardedRow`: a guarded command whose own predicate, beyond
  the key and the version, can still refuse a write at the expected version. `ErrRefused` and
  `*RefusedError[T]` (carrying the row) distinguish this from `ErrVersionMismatch`.
- `Projection.Verify` also probes each declared field's type against a cast, so a misspelled or
  mismatched `--| field:` type fails at startup, not just a missing column.
- The struct-tag mapper (`Scanner`, `ArgsOf`) flattens an untagged embedded struct's fields, so a
  read model reusing a shared entity, identity, or audit type restates nothing.
- `sqlate.ConstraintError` gains `Table` and `Column`, filled when the driver exposes them (a
  not-null violation names no constraint at all, so `Column` is the only handle for that case).
  New `ErrDependentObjects` and `ErrSerializationFailure` sentinels.
- The library's own pattern namespace, `sql`, is reserved: `NewCatalog` refuses any other source
  under it and refuses the library's own source under any other name.

### Changed

- **Breaking:** `Projection.List` takes an explicit `Page` argument and returns `Collection[T]`
  in place of `([]T, int, error)`. Cursor paging is the new `Continue` method, not a field on
  `Directives`.
- **Breaking:** `migrate.New` takes `[]migrate.Set` in place of a single migration list;
  `Options.Table` is removed, since each `Set` carries its own table.

## [v0.1.1] - 2026-09-07

### Fixed

- The session classifies a connectivity failure on every call, not only on `Conn` and
  `Begin`: a network error or `driver.ErrBadConn` from exec, query, prepare, or commit wraps
  `ErrConnectionFailed` before the dialect maps it, so a read against an unreachable engine
  classifies the same way a transaction does. A context deadline or cancellation is not a
  connectivity failure and stays the dialect's to map.

## [v0.1.0] - 2026-09-04

The first release of the SQL templating library, created from the `v1.data.sql.prototype`
experiment's library packages.

### Added

- `sqlate`, the session layer: `DB` over a plain `*sql.DB` through `Wrap`, `Tx` with `Begin`
  and the generic `Transact`, the `Session` method set, the `Dialect` interface, the `Locker`
  and `ErrorMapper` capabilities, and the error taxonomy: `ErrConnectionFailed`,
  `ErrInvalidValue`, the four constraint classes, and `ConstraintError`.
- `header`, the declaration grammar: `Parse` reads the `--|` header of an authored file and
  `End` marks where the body the engine receives begins.
- `query`, authored statements: the pattern catalog (`Publish`, `Patterns`, `As`, `Overlay`,
  `NewCatalog`), `Compile` to `Statements`, `Statement` with `Exec` and the typed values
  `Scan`, `Project`, and `Guarded` produce (`Rows[T]`, `Projection[T]`, `Guard`), the
  struct-tag mapping (`Scanner[T]`, `Scalar[T]`, `ArgsOf`, `Args.With`), `Directives`, list
  expansion, and `Verify`.
- `migrate`, schema versioning over authored SQL: `Migration`, `Files`, `Migrator` with `Up`,
  `Down`, `Steps`, `Force`, `Verify`, and `Version`, the `Catalog` interface with
  `StandardCatalog`, and the protocol's error types.
- `sqltest`, the scripted `database/sql` driver every consumer's unit tier runs over: `Open`,
  `Recorder`, `Response`, and the stub `Dialect`.

[Unreleased]: https://github.com/standards-lab/sqlate/compare/v0.2.0...HEAD
[v0.2.0]: https://github.com/standards-lab/sqlate/compare/v0.1.1...v0.2.0
[v0.1.1]: https://github.com/standards-lab/sqlate/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/standards-lab/sqlate/releases/tag/v0.1.0
