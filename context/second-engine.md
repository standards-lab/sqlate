# A second engine

A second engine is planned as a second sub-module beside `postgres`, named for the engine. The
roadmap tracks it as the backlog goal `second-providers` in `standards-lab/roadmap/index.toml`.

The sub-module carries three parts of its own, each still open:

- a dialect: bind-parameter rendering, error classification, the migrate lock, the version
  statement, and the keyset predicate, as `postgres/doc.go` lists them.
- a `sqlint.toml`.
- an overlay for each library pattern the engine does not accept as written in standard SQL.
