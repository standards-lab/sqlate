# A second engine

A second engine is a second sub-module beside `postgres`, named for the engine. Its roadmap home is
the backlog goal `second-providers` in `standards-lab/roadmap/index.toml`.

Open points:

- The sub-module carries its own dialect: bind-parameter rendering, error classification, the
  migrate lock, the version statement, and the keyset predicate, as `postgres/doc.go` lists them.
- It carries its own `sqlint.toml`.
- It carries an overlay for each library pattern the engine does not accept as written in
  standard SQL.
