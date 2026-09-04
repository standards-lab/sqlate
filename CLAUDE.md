# sqlate

The SQL templating library: authored `.sql` files made dynamic and composable, with the
PostgreSQL dialect in the `postgres` sub-module and the conventions linter in the `sqlint`
sub-module. A standalone library any Go project can adopt, built by the Standards Lab
organization. Managed with the marathon workflow; start from `context/README.md`.

## Where the documentation lives

The user guide is this repository's own: `README.md` is the index, and the `docs/` documents
are read in the order it lists. The organization's
[documentation landing zone](https://github.com/standards-lab/docs) documents the library's
place in the organization's work and is the authority for that. `context/` records only
working knowledge the guide, the landing zone, and the code do not express; do not restate
documented design here. A change that alters documented behavior updates the guide, and the
landing zone page where one exists, in the same effort.

## Repository specifics

- **Module layout.** One base module rooted at `github.com/standards-lab/sqlate`, with the
  `sqlate` package at its root and `header`, `query`, `migrate`, and `sqltest` beside it, plus
  two sub-modules with their own `go.mod`: `postgres`, named for the engine, and `sqlint`,
  the linter with its command at `sqlint/cmd/sqlint`.
- **Dependency line.** The base module imports the standard library alone. A sourced
  dependency enters only through a sub-module's `go.mod`: pgx through `postgres`, the TOML
  parser through `sqlint`.
- **Local development** uses the committed root `go.work`. Pinned `require` versions are the
  committed steady state; a `replace` directive is a transient bridge while a sub-module builds
  against unreleased base changes, and the release drops it.
- **Tests.** The unit tier runs with nothing installed: every suite runs over `sqltest`. The
  integration tier, `mise run integration`, runs the `postgres` proofs against the compose
  stack and is not part of CI; `mise run acceptance` is the one-shot run.
- **Releases, CI, tasks** follow the organization's engineering conventions, documented in
  the landing zone: `v*`, `postgres/v*`, and `sqlint/v*` tags, a per-module CI matrix, mise
  tasks over the modules.
- **Public repo.** Modules resolve through the public Go proxy; CI has no private-module
  configuration.
