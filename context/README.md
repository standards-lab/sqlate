# sqlate

sqlate is the SQL templating library. It makes plain `.sql` files dynamic and composable
through templating instead of replacing them:

- a grammar the files are written in
- a host library that compiles and composes them
- a catalog that sources patterns from several places under namespaces
- an engine sub-module that owns the engine's syntax
- a lint that enforces the conventions

It is a standalone library any Go project can adopt.

The user guide is the repository's own: `README.md` is the index and `docs/` the documents it
orders. The library is adjacent to the organization's Go Elemental standard, and the standard's
[catalog](https://github.com/standards-lab/architecture/blob/main/standards/go-elemental/README.md)
names it as such. This context records only working knowledge the guide and the code do not
express.

## Capability map

- **Session layer** (built): the root package wraps a plain `*sql.DB`, owns transactions, and
  maps every driver error through the engine's dialect ([doc.go](../doc.go)).
- **header** (built): reads the `--|` declaration header of an authored `.sql` file
  ([header/doc.go](../header/doc.go)).
- **query** (built): compiles authored statements against the pattern catalog and binds them to
  typed handles ([query/doc.go](../query/doc.go)).
- **migrate** (built): versions a schema over authored SQL under the engine's lock
  ([migrate/doc.go](../migrate/doc.go)).
- **sqltest** (built): the scripted `database/sql` driver for hermetic tests
  ([sqltest/doc.go](../sqltest/doc.go)).
- **postgres** (built): the PostgreSQL dialect sub-module, with `pgtest` for integration tests'
  throwaway databases ([postgres/doc.go](../postgres/doc.go)).
- **sqlint** (built): the conventions linter sub-module and its command
  ([sqlint/doc.go](../sqlint/doc.go)).
- **Dollar-quoted strings in sqlint** (planned): `sqlint` strips PostgreSQL dollar-quoted bodies
  before it matches code ([dollar-quoting.md](dollar-quoting.md)).
- **A second engine** (planned): a second engine sub-module beside `postgres`
  ([second-engine.md](second-engine.md)).
