# Changelog

All notable changes to the pgvector extension module
(`github.com/standards-lab/sqlate/postgres/pgvector`) are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). This changelog covers this
sub-module only; the base module and the PostgreSQL dialect keep their own.

## [Unreleased]

## [v0.1.0] - 2026-11-24

The first release of the pgvector extension module. It imports only the standard library.

### Added

- `Extension`, pgvector's catalog name, `vector`.
- `Metric`, with `Name`, `Operator`, and `OpClass`, and the metric table of the `vector` type:
  `L2` (`l2`: `<->`, `vector_l2_ops`), `Cosine` (`cosine`: `<=>`, `vector_cosine_ops`), and
  `InnerProduct` (`inner product`: `<#>`, `vector_ip_ops`), returned in that order by `Metrics()`.
- `sqlint.toml` exporting pgvector's native forms for sqlint's `extensions` key: the six
  distance operators, the `vector` type where a type goes and `halfvec` and `sparsevec`
  anywhere, `USING hnsw` and `USING ivfflat`, fifteen functions called by name, and the
  `hnsw.*` and `ivfflat.*` settings.

Requires `github.com/standards-lab/sqlate v0.8.0` and
`github.com/standards-lab/sqlate/postgres v0.9.0` for the integration tier only; the package
imports neither.

[Unreleased]: https://github.com/standards-lab/sqlate/compare/postgres/pgvector/v0.1.0...HEAD
[v0.1.0]: https://github.com/standards-lab/sqlate/releases/tag/postgres/pgvector/v0.1.0
