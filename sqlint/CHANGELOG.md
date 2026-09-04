# Changelog

All notable changes to the conventions lint (`github.com/standards-lab/sqlate/sqlint`) are
documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the module adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This
changelog covers this sub-module only; the base module keeps its own.

## [Unreleased]

The first release of the lint, against `github.com/standards-lab/sqlate v0.1.0`.

### Added

- `sqlint`, the package: `Config` and `Load` for `sqlint.toml`, one file per module at its
  root; `Resolver` with `GoList` over `go list -m`; `Lint` returning `Finding` values; the
  checks per role: statements compile against the catalog, patterns validate as a source, a
  file is named for its operation, the delimiter stays out of comments and literals, a
  standard-tier file uses no native form, and a non-transactional migration contains exactly
  one statement.
- `cmd/sqlint`, the command: one root argument, findings as `path:line: message`, exit 1 on
  any finding.
