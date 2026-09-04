# Changelog

All notable changes to the PostgreSQL dialect (`github.com/standards-lab/sqlate/postgres`) are
documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the module adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This
changelog covers this sub-module only; the base module keeps its own.

## [Unreleased]

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

[Unreleased]: https://github.com/standards-lab/sqlate/compare/postgres/v0.1.0...HEAD
[v0.1.0]: https://github.com/standards-lab/sqlate/releases/tag/postgres/v0.1.0
