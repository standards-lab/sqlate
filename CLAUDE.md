# sqlate

sqlate is the SQL templating library: authored `.sql` files made dynamic and composable, with
the PostgreSQL dialect in the `postgres` sub-module and the conventions linter in the `sqlint`
sub-module. It is a standalone library any Go project can adopt, built by the Standards Lab
organization. The repository is managed with the marathon workflow; start from
`context/README.md`.

## Where the documentation lives

The user guide is this repository's own: `README.md` is the index, and the `docs/` documents
are read in the order it lists. The library is adjacent to the organization's Go Elemental
standard rather than a member of it, and the organization's
[architecture repository](https://github.com/standards-lab/architecture) names it as such in the standard's catalog. `context/` records
only working knowledge the guide and the code do not express; do not restate documented design
here. A change that alters documented behavior updates the guide in the same effort.

## Repository specifics

- **Module layout.** The base module is rooted at `github.com/standards-lab/sqlate`, with
  `sqlate` at its root and `header`, `query`, `migrate`, and `sqltest` beside it. Two
  sub-modules have their own `go.mod`: `postgres` names the engine, and `sqlint` is the linter,
  with its command at `sqlint/cmd/sqlint`.
- **Dependency line.** The base module imports the standard library alone. A sourced
  dependency enters only through a sub-module's `go.mod`: pgx through `postgres`, the TOML
  parser through `sqlint`.
- **Local development** uses the committed root `go.work`. Pinned `require` versions are the
  committed steady state; a `replace` directive is a transient bridge while a sub-module builds
  against unreleased base changes, and the release drops it.
- **Tests.** The unit tier runs with nothing installed: every suite runs over `sqltest`. The
  integration tier, `mise run integration`, runs the `postgres` proofs against the compose
  stack and is not part of CI; `mise run acceptance` is the one-shot run.
- **Releases, CI, tasks** follow the organization's engineering conventions, the Go Elemental
  principles in the architecture repository: `v*`, `postgres/v*`, and `sqlint/v*` tags, a per-module CI matrix, mise
  tasks over the modules.
- **Public repo.** Modules resolve through the public Go proxy; CI has no private-module
  configuration.
