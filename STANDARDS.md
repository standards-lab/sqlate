# sqlate standards

The judgement calls the standards-reviewer applies to sqlate.

- A change that alters documented behavior updates the guide, `README.md` and `docs/`, in the same change.
- `architecture/standards/go-elemental/principles/dependencies.md`: the bottom-up hierarchy and no provider in a base, across the three modules' `go.mod` files.
- `architecture/standards/go-elemental/principles/tests-and-docs.md`: the doc.go inventory of every package, and the unit tier over `sqltest`.
- `architecture/standards/go-elemental/principles/topology-and-naming.md`: the `postgres` and `sqlint` sub-modules and their tags.
- `architecture/standards/go-elemental/principles/release-and-ci.md`: the check, currency, and a changelog per module.
- `architecture/standards/go-elemental/principles/dsl-driven-services.md`: `query`, `header`, and every authored `.sql` file, the library's patterns included.
- `architecture/standards/go-elemental/principles/baseline-standards.md`: `query`'s guards and projections, which take every name and bound from their caller.
- `architecture/principles/service-tiers.md`: the `Dialect` boundary between the base module and each engine sub-module.
- `architecture/principles/tool-beside-library.md`: the `sqlint` package and its command.
- `architecture/principles/validation-first.md`: beyond authored SQL, `sqlint.Load` and `migrate.New` validate before any effect.
- `architecture/principles/context-architecture.md`: the guide and each `doc.go` are the homes; nothing else restates them.
