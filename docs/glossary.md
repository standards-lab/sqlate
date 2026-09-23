# Glossary

The library's vocabulary, grouped by the layer each term belongs to. Two operations produce
statements, compile and compose, and one consumes them, execute.

## Files

SQL as written, never sent as written.

- **`.sql` file**: a file the library reads: a statement, a pattern, or a migration.
- **Header**: a `.sql` file's leading `--|` declarations. The compiler reads the header and
  the engine never sees it.
- **Declaration**: one `--| key: value` line of the header, such as `tier`, `native`, `port`,
  `transaction`, `key`, or `field`. A long value folds onto following lines written
  `--| [key]: more`.
- **Body**: what follows the header; what the engine receives.
- **Tier**: the portability a file declares. A standard file uses only standard SQL and runs on
  any engine; a native file uses a feature only the named engine supports, and it names that
  feature and the port.
- **Port**: how a native file's effect is reached on other engines, stated in its `native`
  declaration or in a `port` declaration of its own.
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
  is `sql`, and no other source may register under it.

## Catalog

Built once, where the program starts, and read-only after.

- **Source**: one namespace's patterns as declared: a directory, plus any overlays.
- **Overlay**: an engine's replacement of a source's patterns by name, with the same
  parameters or the pattern's declared `alternate` set.
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
- **Key**: the base's identity column and sort tie-breaker. A composite key is several columns,
  which tie-break in the order the header declares them.
- **Field**: a column a request may filter or sort by, with the SQL type the request's value is
  cast to, and `not null` when the column never holds a null. The declared fields are the field
  contract.
- **Guard** (`query.Guard`): a guarded command bound to its version check.
- **Returning command** (`query.Returning[T]`): a standard-tier `INSERT INTO` or `UPDATE` that
  names the read of its changed row. It runs in its single-statement form where the dialect
  returns rows, and as the fallback elsewhere.
- **Read**: the statement a returning command names to read its changed row back. Its column list
  is the list of returned columns.
- **Single-statement form**: a returning command as the dialect renders it, with the engine's
  clause (`RETURNING`) appended, so the command itself returns the changed row.
- **Fallback**: a returning command run as the command and then its read, in one transaction.
- **Row guard** (`query.RowGuard[T]`): a guard over a returning command whose own predicate can
  refuse a row at the expected version. It returns the changed row.
- **Command**: a statement that mutates rows.
- **Check**: the statement a `Guard` runs to read a row's current version by key. A row guard has
  none: it reads the row with its command's read.
- **Scan function**: the function that reads one row into a `T`. `Scanner[T]` derives one from
  the entity's struct tags; `Scalar[T]` reads a one-column row. It reads the row through
  `query.Row`, never `*sql.Rows` itself.

## Execution

- **Directives**: a collection read's request: its sorts, its filters, and whether it counts
  the total, which the page's own statement reads. The page is a separate argument: a `Page` to `List`, or a cursor and a size to
  `Continue`.
- **Collection** (`query.Collection[T]`): one page of a collection read: its items, the total,
  whether a further page exists, and the cursor that continues past it.
- **Keyed prefix**: the shortest run of a read's sort terms, from the first, that includes every
  key field. Those terms order the rows uniquely, so a cursor records the values of its row for
  them.
- **Cursorable**: said of an ordering whose keyed prefix sorts in one direction over fields
  declared `not null`; only a cursorable read issues a cursor.
- **Cursor** (`query.Cursor`): an opaque position in a read's ordering, the keyed values of a
  page's last row, that `Continue` reads the next page from.
- **Signature**: a read's shape with the values removed: the field and operator pairs, the sort
  terms, and whether it reads by offset or continues by cursor. The composed SQL depends on the
  signature alone.
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
