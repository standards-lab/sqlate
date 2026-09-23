# Changelog

All notable changes to the conventions linter (`github.com/standards-lab/sqlate/sqlint`) are
documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the module adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This
changelog covers this sub-module only; the base module keeps its own.

## [Unreleased]

### Added

- Lint of the `returning` header key. A statement directory compiles against a base module
  that knows the key, so a returning command whose read is missing, whose verb is not
  `INSERT INTO` or `UPDATE`, or whose read is not a plain column list is a compile finding
  against its directory. `RETURNING` written directly into a standard file is still a native
  form.

Requires `github.com/standards-lab/sqlate v0.3.0`; a linter pinned to an earlier base reports
the key as an unknown declaration.

## [v0.1.2] - 2026-09-22

### Fixed

- The library's own namespace, `sql`, resolves to the patterns the library embeds, whatever
  path a `[sources] sql = ...` entry names. Before, the linter published that path under `sql`,
  the catalog refused it as a namespace only the library may register, and every statement
  directory went unchecked behind that one finding. The entry's path is now not read, so an
  existing entry can stay as it is; its `overlay`, when declared, still applies to the
  library's patterns.

Requires `github.com/standards-lab/sqlate v0.2.0`, whose reservation of the `sql` namespace this
fix accounts for; the defect does not arise against an earlier base version.

## [v0.1.1] - 2026-09-10

### Added

- The `guard` check of the statements role, on by default: a statement with a `SET` list that
  includes `guard_where` also includes `guard_set`, and a statement that includes `guard_set`
  also includes `guard_where`. The guard's check and a guarded delete include `guard_where`
  alone and pass. The include is matched by pattern name under any namespace, so an overlay or
  another source that publishes the guard patterns is recognized.

## [v0.1.0] - 2026-09-04

The first release of the linter, against `github.com/standards-lab/sqlate v0.1.0`.

### Added

- `sqlint`, the package: `Config` and `Load` for `sqlint.toml`, one file per module at its
  root; `Resolver` with `GoList` over `go list -m`; `Lint` returning `Finding` values; the
  checks per role: statements compile against the catalog, patterns validate as a source, a
  file is named for its operation, the delimiter stays out of comments and literals, a
  standard-tier file uses no native form, and a non-transactional migration contains exactly
  one statement.
- `cmd/sqlint`, the command: one root argument, findings as `path:line: message`, exit 1 on
  any finding.

[Unreleased]: https://github.com/standards-lab/sqlate/compare/sqlint/v0.1.2...HEAD
[v0.1.2]: https://github.com/standards-lab/sqlate/compare/sqlint/v0.1.1...sqlint/v0.1.2
[v0.1.1]: https://github.com/standards-lab/sqlate/compare/sqlint/v0.1.0...sqlint/v0.1.1
[v0.1.0]: https://github.com/standards-lab/sqlate/releases/tag/sqlint/v0.1.0
