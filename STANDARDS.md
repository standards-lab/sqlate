# sqlate standards

The judgement calls the standards-reviewer applies to sqlate, beyond what `mise run check` enforces.

- A change that alters documented behavior updates the guide, `README.md` and `docs/`, in the same change.
- `architecture/standards/go-elemental/principles/dependencies.md`: the bottom-up line and no provider in a base, across the three modules' `go.mod` files.
- `architecture/standards/go-elemental/principles/tests-and-docs.md`: the doc.go inventory of every package, and the unit tier over `sqltest`.
- `architecture/standards/go-elemental/principles/topology-and-naming.md`: the base module and the `postgres` and `sqlint` sub-modules, and their tags.
- `architecture/standards/go-elemental/principles/release-and-ci.md`: the check, currency, and a changelog per module.
- `architecture/standards/go-elemental/principles/dsl-driven-services.md`: `query`, `header`, and every authored `.sql` file, the library's patterns included.
- `architecture/standards/go-elemental/principles/baseline-standards.md`: `query`'s guards and projections, which take every name and bound from their caller.
- `architecture/standards/go-elemental/principles/utc-times.md`: `query.Scanner` and `query.Scalar`, which return every time in UTC, a cursor's keyed time, and the `postgres` dialect's `timestamp with time zone` history.
- `architecture/principles/service-tiers.md`: the `Dialect` boundary between the base module and each engine sub-module.
- `architecture/principles/tool-beside-library.md`: the `sqlint` package and its command.
- `architecture/principles/validation-first.md`: authored SQL, `sqlint.Load` and `migrate.New`.
- `architecture/principles/context-architecture.md`: the guide and each `doc.go` are the homes.
