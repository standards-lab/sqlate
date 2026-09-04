# Changelog

All notable changes to `github.com/standards-lab/sqlate` are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). This changelog covers the base
module only; the `postgres` and `sqlint` sub-modules each keep their own.

## [Unreleased]

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
