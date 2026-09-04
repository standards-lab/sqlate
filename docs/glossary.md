# Glossary

The library's vocabulary, grouped by the layer each term belongs to. Two operations produce
statements, compile and compose, and one consumes them, execute.

## Files

SQL as written, never sent as written.

- **`.sql` file**: a file the library reads: a statement, a pattern, or a migration.
- **Header**: a `.sql` file's leading `--|` declarations. The compiler reads the header and
  the engine never sees it.
- **Declaration**: one `--| key: value` line of the header: `tier`, `native`, `transaction`,
  `key`, or `field`.
- **Body**: what follows the header; what the engine receives.
- **Tier**: the portability a file declares. A standard file uses only standard SQL and runs on
  any engine; a native file uses a feature only the named engine supports, and it names that
  feature and the port.
- **Port**: how a native file's effect is reached on other engines, stated in its `native`
  declaration.
- **Statement**: a `.sql` file that expresses one operation and may include patterns, compiled
  to SQL text with parameters and no values.
- **Parameter**: a statement's named input, `{{name}}` or `{{name:type}}`.
- **List expansion**: a parameter written `{{name...}}`, which binds a non-empty slice as one
  placeholder per element.
- **Pattern**: a reusable template that many statements use, published under a namespace: a
  `.sql` file whose body contains parameters only, never an include.
- **Include**: a statement's reference to a pattern, `{{> namespace.name}}`; the library splices
  the pattern's text in at compile time.
- **Namespace**: the name a source's patterns are published under, and what keeps one source's
  patterns apart from another's: an include names its pattern's namespace. The library's own
  is `sql`.

## Catalog

Built once, where the program starts, and read-only after.

- **Source**: one namespace's patterns as declared: a directory, plus any overlays.
- **Overlay**: an engine's replacement of a source's patterns by name, with the same
  parameters.
- **Alias**: a source registered under a namespace other than its default.
- **Catalog**: the set of registered sources, read and validated; every statement compiles
  against it.

## Statements

- **Compile**: the operation that turns a `.sql` file into a statement against the catalog
  and the dialect, with includes spliced and parameters rendered as the engine's placeholders.
- **Statements** (`query.Statements`): a set of related statements compiled from one
  directory.
- **Verify**: the operation that prepares each statement, and each projection's field contract,
  against the live schema at startup.

## Typed values

A statement bound, once, to the code that runs it. A program keeps these values and never SQL
text.

- **Rows** (`query.Rows[T]`): a statement bound to a scan function; it returns rows.
- **Projection** (`query.Projection[T]`): a base bound to a scan function; it runs the
  collection read and the single-row read.
- **Base**: the authored query the collection read wraps as a derived table. It declares its
  key and its fields.
- **Key**: the base's identity column and sort tie-breaker.
- **Field**: a column a request may filter or sort by, with the SQL type the request's value is
  cast to. The declared fields are the field contract.
- **Guard** (`query.Guard`): a guarded command bound to its version check.
- **Command**: a statement that mutates rows.
- **Check**: the statement a guard runs to read a row's current version by key.
- **Scan function**: the function that reads one row into a `T`. `Scanner[T]` derives one from
  the entity's struct tags; `Scalar[T]` reads a one-column row.

## Execution

- **Directives**: a collection read's request: its page, sorts, and filters.
- **Signature**: the directives with the values removed: the field and operator pairs, the sort
  terms, and the paging flag. The composed SQL depends on the signature alone.
- **Compose**: the operation that turns a base, the catalog's patterns, and a signature into a
  statement at request time.
- **Arguments**: the values a request binds to parameters by name, `query.Args`.
- **Execute**: the operation that runs a statement with its arguments through a session.
- **Session**: the pool or a transaction: `*sqlate.DB` or `*sqlate.Tx`, both of which implement
  `sqlate.Session`.
- **Engine**: the database server the driver sends SQL to, PostgreSQL through the `postgres`
  sub-module. A file's body is what the engine receives.
- **Dialect**: the engine's name, its placeholder syntax, and its error classification, in a
  sub-module named for the engine. Every error is mapped through it.
- **Capability**: an interface a protocol asserts on the dialect beyond `Dialect` itself:
  `Locker` for the migration lock, `ErrorMapper` on a session.

## Lint

- **Role**: one of the three kinds of directory the linter checks: statements, patterns, and
  migrations. Each role has its checks and its switches.
- **Producer**: a module, or a directory with its own `sqlint.toml`, whose `[export]` table
  declares what a consumer reads from it: a pattern directory, an overlay directory, native
  forms.
- **Native form**: one entry of the deny list an engine exports: a regular expression, keyed
  by a name, that detects one of the engine's native operations in a standard-tier file. A
  match fails the linter under that name.
- **Finding**: one convention a file fails: the path, the line when the check has one, and the
  message.
