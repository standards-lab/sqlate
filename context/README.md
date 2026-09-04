# sqlate

The SQL templating library. It makes plain `.sql` files dynamic and composable through
templating instead of replacing them: a grammar the files are written in, a host library that
compiles and composes them, a catalog that sources patterns from several places under
namespaces, an engine sub-module that owns the engine's syntax, and a lint that enforces the
conventions. It is a standalone library any Go project can adopt, and it imports only the
standard library at its root.

The user guide is the repository's own: `README.md` is the index and `docs/` the documents it
orders. The organization's [documentation landing zone](https://github.com/standards-lab/docs)
documents the library's place in the organization's work; the pages that describe it there
are the docs pass's to write. This context records only working knowledge the guide,
the landing zone, and the code do not express.

## Capability map

The built packages are authoritative through their code and `doc.go`; the guide documents
their use. Detail for what is unbuilt is added when it is about to be built.

- **sqlate** (the root) is the session layer: `DB` over a plain `*sql.DB`, `Tx` and
  `Transact`, the `Session` method set, the `Dialect` interface and its capabilities, and the
  error taxonomy every engine returns. Built.
- **header** reads the `--|` declaration header of an authored file and knows no keys. Built.
- **query** compiles authored statements against the pattern catalog and binds them to typed
  values: rows, projections with request directives, and guards. Built.
- **migrate** versions a schema over authored SQL under the dialect's lock. Built.
- **sqltest** is the scripted driver every consumer's unit tier runs over. Built.
- **postgres** (sub-module) is the PostgreSQL dialect: placeholders, error classification, the
  advisory lock, the exported native forms, and the integration tier's proofs. Built.
- **sqlint** (sub-module) is the conventions linter as a package with a thin command. Built.

A second engine is a second sub-module named for the engine, with its own dialect and
`sqlint.toml`, and an overlay for each library pattern it does not accept as written in
standard SQL.
