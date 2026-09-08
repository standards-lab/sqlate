# sqlate

The SQL templating library. It makes plain `.sql` files dynamic and composable through
templating instead of replacing them: a grammar the files are written in, a host library that
compiles and composes them, a catalog that sources patterns from several places under
namespaces, an engine sub-module that owns the engine's syntax, and a lint that enforces the
conventions. It is a standalone library any Go project can adopt.

The user guide is the repository's own: `README.md` is the index and `docs/` the documents it
orders. The library is adjacent to the organization's Go Elemental standard, and the standard's
[catalog](https://github.com/standards-lab/architecture/blob/main/standards/go-elemental/README.md) names it as such. This context records only working knowledge the guide and
the code do not express.

## Capability map

Every package is built: the session layer at the root, `header`, `query`, `migrate`, and
`sqltest` in the base module, and the `postgres` and `sqlint` sub-modules. The README's
Packages section lists them, the code and each package's `doc.go` are authoritative for the
API, and the guide documents their use. Detail for what is unbuilt is added when it is about
to be built.

A second engine is a second sub-module named for the engine, with its own dialect and
`sqlint.toml`, and an overlay for each library pattern it does not accept as written in
standard SQL.
