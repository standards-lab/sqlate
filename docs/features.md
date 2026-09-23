# Features

Every feature, package by package, in the detail needed to use it without reading the source.
The package documentation (`go doc`) is the reference for each name's exact signature.

The packages form two layers. The root package is the session layer every statement runs
through. `query` and `migrate` are the two capabilities built over it: authored statements, and
schema versioning that replaces a third-party migration dependency. `header` is the grammar
both read, `sqltest` is the driver their tests run over, and the two sub-modules are the
engine and the linter.

## sqlate: the session layer

**Wrap.** `sqlate.Wrap(pool, dialect)` builds a `*DB` over a plain `*sql.DB` and the engine's
`Dialect`. It performs no I/O and adds nothing to the pool's lifecycle: opening, readiness, and
closing belong to whoever owns the pool. A nil pool or dialect is a defect in the caller and
panics.

**Session.** `Session` is the method set every typed value takes: `ExecContext`,
`QueryContext`, and `PrepareContext`. `*DB` and `*Tx` both implement it, so one value runs
against the pool or inside a transaction. Every method maps the driver's error through the
dialect before returning it. `ErrorMapper` is the capability both expose for errors that arise
after a call returns, from `rows.Err` and `Scan`; the typed values use it, and a consumer
running raw SQL through a session can too.

**Transactions.** `DB.Begin(ctx, opts...)` opens a `*Tx` with `TxOption` values applied:
`Isolation(level)` and `ReadOnly()`. `Tx.Commit` maps its error, the one place a violation
deferred to `COMMIT` can be classified; `Tx.Rollback` does not. `DB.Transact(ctx, fn, opts...)`
is the runner: it begins, calls `fn(tx)`, commits on success, and returns `fn`'s result. On
`fn`'s error it rolls back and returns that error with a rollback failure joined onto it. A
panic in `fn` rolls back and re-panics, so no transaction leaks to the pool. `Beginner` is the
interface of a session that can open a transaction, satisfied by `*DB` and any type embedding
it. A protocol handed a session rather than a transaction asserts `Beginner` to open its own.
`sqlate.Transact(ctx, b, fn, opts...)` is the same runner over any `Beginner`, so a protocol, or
an application's own pool type embedding `*DB`, runs its unit exactly as `DB.Transact` does;
`DB.Transact` is `Transact` over the `*DB`.

**Pinned connections.** `DB.Conn(ctx)` pins one connection for a protocol that needs session
scope: a session-level lock, or DDL an engine refuses inside a transaction. The caller closes
it.

**Dialect.** `Dialect` is what the library needs from an engine: `Name`, `Placeholder(n)`, the
engine's syntax for the nth bind parameter, and `MapError`, the classification of a driver
error into the library's sentinels. A sub-module named for the engine implements it. `Locker`
is a capability a dialect may add: `Lock` and `Unlock` of a named session-level lock on a
pinned connection; `migrate` asserts it. `query.Returner` is another: it renders a returning
command as one statement, and `Compile` asserts it.

**Errors.** The root package defines the errors a consumer matches on, and every package keeps
its own sentinels and error types in one file, `errors.go`, so a package's whole taxonomy is
read in one place.

| Error | Meaning | Match with |
|---|---|---|
| `ErrConnectionFailed` | A connection or a transaction could not be obtained. Wraps the driver's error. | `errors.Is` |
| `ErrInvalidValue` | A data exception: a bound value the engine could not read as the type it was cast to (SQLSTATE class 22). Wraps the driver's error. | `errors.Is` |
| `ErrUniqueViolation`, `ErrForeignKeyViolation`, `ErrCheckViolation`, `ErrNotNullViolation` | The four constraint classes. | `errors.Is` on the class |
| `ConstraintError` | The value a dialect returns for a constraint violation: `Class` (one of the four), `Err` (the driver's error), and `Constraint`, `Table`, and `Column`, the violated constraint's name and the table and column it names, each filled when the driver exposes it. A not-null violation names no constraint, so `Column` is its only handle. | `errors.As` |
| `ErrDependentObjects` | A drop the engine refused because another object still depends on the object dropped, such as a set's revert while a foreign key from a set above still references it. Wraps the driver's error. | `errors.Is` |
| `ErrSerializationFailure` | A transaction the engine aborted under `SERIALIZABLE` isolation because it could not be serialized with a concurrent one. Wraps the driver's error. | `errors.Is` |

`errors.As` finds the driver's error through every wrap. `sql.ErrNoRows` is never mapped.

## header: the declaration grammar

`header.Parse(text)` reads the header of a `.sql` file: the leading run of blank lines,
plain `--` comments (prose, skipped), and `--|` declaration lines of the form `--| key: value`.
The header ends at the first line that is none of those. A `--|` line that is not a
declaration is an error, and a declaration after the body has begun is an error. `Header.End()`
is the byte offset where the body begins; `Declarations`, `Get`, `All`, and `Keys` read the
declarations. The package knows no keys; each consumer decides which it accepts.

A long value folds across lines: a line `--| [key]: more` repeats in brackets the key of the
declaration directly above it, and `Parse` appends its text to that declaration's value with
one space between the pieces. A blank line, a prose line, or another declaration ends the run a
fold may continue, and a fold whose key differs from the declaration it continues, or that
continues nothing, is an error.

```sql
--| tier: native
--| native: postgres, the row-value comparison (a, b) > (x, y).
--| [native]: The standard tier spells it as a chain of disjuncts.
```

The keys `query` and `migrate` accept:

| Key | Files | Meaning |
|---|---|---|
| `tier` | statements, patterns | Required: `standard` or `native`. |
| `native` | statements, patterns | Required when the tier is native: the engine feature used and the port, as free text. |
| `port` | statements | Optional, native tier only: the port as its own declaration, as free text. |
| `alternate` | patterns | Optional: the comma-separated slots an overlay may declare in place of the pattern's own. |
| `transaction` | statements | `required`: the statement refuses to run outside a transaction. |
| `transaction` | migrations | `none`: the migration runs outside a transaction; `required` or absent keeps one. |
| `key` | projection bases | The identity column and sort tie-breaker: one declared field, or several separated by commas (`id` or `org, id`), each named once. |
| `returning` | statements | Optional, on a standard-tier `INSERT INTO` or `UPDATE` only: the name of the statement in the same directory that reads the changed row back. |
| `field` | projection bases | One per column a request may filter or sort by: `<name> <sql type>`, with an optional trailing `not null`, matched case-insensitively, for a column that never holds a null. |

## query: authored statements

### The catalog

`Publish(namespace, fsys, dir)` declares the `.sql` files under a directory as the patterns of
a namespace; nothing is read until the catalog is built. `Patterns()` is the library's own
source under the namespace `sql`. The namespace is reserved: only the library's source
registers under it, a source published or aliased as `sql` is refused, and the library's
source is refused under any other name. `Source.As(namespace)` registers a source under an
alias, and `Source.Overlay(fsys, dir)` replaces its patterns by name with an engine's own; a
later overlay wins over an earlier one.

A pattern may declare `--| alternate:`, a second set of slots an overlay may fill in place of
its own. The library's keyset predicate uses it: its standard body is a chain of disjuncts over
one slot, and its alternate set is the column list, the operator, and the value list an engine
needs to spell it as a row-value comparison.

`NewCatalog(sources...)` reads every source and validates it:

- each file declares a tier, and a native file names its port
- a pattern includes no other pattern
- an overlay respells only what its source defines, with the same slots or the pattern's
  alternate set
- no two sources share a namespace
- only the library's source registers under `sql`, and it registers under no other namespace

Every failure is reported, joined, each naming the namespace and file. `MustCatalog` panics
instead, for the place a program starts. `Catalog.Namespaces()` and `Catalog.Patterns()` list
the inventory, so a program can report what it compiled against; each `Pattern` reports its
`Slots` and its `Alternate` slots.

### Compilation

`Catalog.Compile(fsys, dir, dialect)` reads every `.sql` file under a directory, parses its
header, splices its includes from the catalog, and renders its parameters as the dialect's
placeholders in order of appearance. A file without a header, with an unknown declaration, with
a header the grammar rejects, or with an include the catalog cannot resolve is a load error
naming the file. So is an invalid returning declaration: a command that is not a standard-tier
`INSERT INTO` or `UPDATE`, or that declares a key or field; or a read that is missing, declares
`returning`, a key, a field, or `transaction: required`, takes a parameter the command does not,
is not `SELECT <column>, … FROM …` with every column lowercase, under one qualifier or none, and
named once, or reads a table other than the one the command changes; and a dialect whose
single-statement form introduces a `{{`. The read's table is the first after its `FROM`, with its
optional alias (`FROM t`, `FROM t x`, `FROM t AS x`, `FROM s.t`); it must be the name after the
command's `INSERT INTO` or `UPDATE`, a schema-qualified name compared as written, and a qualifier
on the read's columns must be that table's alias or name
(`returning read "doc_row" reads "users"; the command changes "docs"`).
`MustCompile` panics instead. The result is a `*Statements`, the directory's inventory:
`Statement(name)` returns the statement named by its file's base name and panics on a missing
one, `Statements()` lists them in name order, and `Verify` prepares each against a session.

A `Statement` reports what its file declared: `Name`, `Text` (the body as the engine receives
it, less a trailing semicolon), `Tier`, `Native`, `Port`, `TransactionRequired`, `Key` (the
declared key, a composite key's parts joined by `", "`), `Keys` (the key's parts in
header order), `Fields` (each with `Name`, `Type`, and `NotNull`), `Params` (the parameter names
in position order), and `Catalog` (the catalog it compiled against). A returning command also
reports `Reads` (the read's name) and `ReturningText` (the single-statement form as the engine
receives it, empty where the dialect declined).

### Parameters and casts

A parameter is `{{name}}`, whitespace inside the braces allowed. `{{name:type}}` binds it
through `CAST(placeholder AS type)`; the type is written into the SQL verbatim, standard or the
engine's own as the file's tier declares, and `Verify` catches a type name the engine does not
know. `{{name...}}` and `{{name...:type}}` expand: the argument is a non-empty slice, and the
parameter renders as one placeholder per element, so an `IN` list binds as values and never as
text. The text is rendered per element count and cached by it. A name has one expansion form
throughout a file, while a type belongs to each occurrence.

The delimiter is reserved: `{{` means a parameter or an include wherever it appears in the
body, string literals and comments included, so the body needs no lexer. A `{{` that forms
neither is a load error.

### Includes

`{{> namespace.name}}` splices a pattern's body into the statement at compile time, as if
authored there. An unqualified include, an unknown namespace or pattern, or a native pattern
inside a standard-tier statement is a load error naming the statement. A pattern's parameters
pass through the include and compile as parameters of the including statement.

### Typed values

A statement is bound once, in a constructor, to the value that runs it. Each value takes a
`sqlate.Session` per call, so it runs against the pool or inside a transaction alike, and every
error passes through the session's mapper.

| Method on `Statement` | Value | Methods |
|---|---|---|
| `Exec(ctx, session, args)` | none | Returns the rows affected. |
| `Scan(scan)` | `Rows[T]` | `One` returns the first row (`sql.ErrNoRows` when none); `All` returns every row; `Each` yields rows one at a time as an `iter.Seq2[T, error]`, closing the row set when the loop ends. |
| `Project(scan)` | `Projection[T]` | `List(ctx, session, directives, page, base...)` reads one page by offset and returns a `Collection[T]`; `Continue(ctx, session, directives, after, size, base...)` reads the `size` rows past a previous page's cursor and returns the same; `One(ctx, session, field, value, base...)` is the base under one equality predicate; `Verify` probes the field contract. A base without a key or field contract, or one with an expanded parameter (`{{name...}}`), panics at binding; a base's other parameters bind from the `base` arguments. |
| `Guarded(check, version)` | `Guard` | `Run(ctx, session, version, args)` binds the expected version under the named parameter, runs the command, and returns the new version; when the command changed nothing it runs the check: no row is `sql.ErrNoRows`, a row is `ErrVersionMismatch` wrapping the expected and current versions. |
| `Returning(scan)` | `Returning[T]` | For a returning command. `One(ctx, session, args)` runs the command and returns the row as it stands afterward and whether the command changed it: no row at all is `sql.ErrNoRows`; a command that changed more than one row, or whose read does not find the row it changed, is `ErrNotOneRow`. `Guarded(version, current)` returns a `RowGuard[T]`. |
| `Returning(scan).Guarded(version, current)` | `RowGuard[T]` | For a guarded command whose own predicate, beyond the key and the version, can refuse a row. `current` reads a row's version. `Run(ctx, session, version, args)` returns the changed row; when the command changed nothing it classifies the row its read found: no row is `sql.ErrNoRows`, a row at another version is `ErrVersionMismatch`, and a row at the expected version is a `*RefusedError[T]` carrying the row, which unwraps to `ErrRefused`. |

A statement headed `transaction: required` refuses to run against `*DB` with
`ErrTransactionRequired`.

A returning command runs in one of two forms, chosen when it compiles. Where the dialect
implements `query.Returner` and accepts the command, the command runs in its single-statement
form, which returns the changed row, and the read runs only when nothing changed. Otherwise the
command runs the fallback: the command and then its read, as one unit. The unit is the session
itself when it is a `*Tx`, or a transaction `One` opens and commits when the session is a
`sqlate.Beginner`; on any other session `One` returns `ErrTransactionRequired`.

For a sound declaration, a read that selects exactly the row the command changed from the
command's own table, both forms return the same row, and only the statement count differs. The
load checks the table; the read's `WHERE` is the author's. The forms differ in three cases:

- The command changes more than one row on a session that is not a transaction. The
  single-statement form has already committed the change when it returns `ErrNotOneRow`; the
  fallback's own transaction rolls it back. Inside a caller's `*Tx`, the caller decides.
- The read does not find the row the command changed, for a filter the command does not share.
  The single-statement form returns the row `RETURNING` gave; the fallback returns
  `ErrNotOneRow`.
- The session is neither a `*sqlate.Tx` nor a `sqlate.Beginner`. Only the single-statement form
  runs; the fallback returns `ErrTransactionRequired`.

A command whose key the engine generates, an `INSERT` that leaves an identity column or a
sequence default to the engine, cannot be a returning command: the read's parameters must be the
command's, so the read has no way to name the row the engine keyed. The caller supplies the key,
minted in the program, and the read finds the row by it.

### Struct-tag mapping

An entity's tags are its scan and binding contract, so a program writes neither scan functions
nor argument literals. A field's column name is its `db` tag, else its `json` tag's name, else
the field name lowercased; `db:"-"` excludes it.

An untagged embedded struct flattens: its fields become columns of the outer type, as if
declared there, so a read model that embeds a shared identity or audit type restates none of its
fields. A field of the outer type shadows a field of the same name reached through an embedded
struct, and between two embedded structs that offer one name, the one declared first wins. An
embedded struct with a `db` or `json` tag is one column holding the whole embedded value
instead. An embedded pointer contributes no column. The embedded type must be exported, since
reflection cannot reach an unexported field.

- `Scanner[T]()` returns the `ScanFunc[T]` for `T`: each row's columns are matched to fields by
  name and scanned into a fresh `T`. A column `T` has no field for is an error, so a `SELECT`
  list that grows past its entity fails; a field with no column stays zero.
- `Scalar[T]` is the scan function for a single-column row.
- A `ScanFunc[T]` reads its row through `Row`, the two methods a scan needs: `Columns` and
  `Scan`. `*sql.Rows` satisfies it, and so does the adapter the collection read hands a scan to
  keep its total column out of sight, so a scan relies on nothing beyond `Row` and reads the row
  with one `Scan`.
- `ArgsOf(v)` binds a struct's fields as `Args` by their column names; a nil pointer binds
  `NULL`.
- `Args` is `map[string]any`. A missing name is an `ArgumentError`, a programming error rather
  than request input; an extra name is ignored, so one map serves a guard's command and its
  narrower check. `Args.With(name, value)` returns a copy with one more binding, for an input
  that arrives separately from the command's fields, such as the row's id. `query.With(name,
  value)` starts a chain with one binding, for a projection base's own parameters.

### Directives and composition

`Directives` is one read request against a projection: `Sort` (a list of
`{Field, Descending}`), `Filters` (a list of `{Field, Op, Value}`), and `Total`, a `TotalMode`.
Field names reference the base's declared fields; an unknown name is rejected as an
`UnknownFieldError` before any SQL is composed. `TotalExact`, the zero value, counts the rows
under the filters in the page's own statement; `TotalNone` skips the count.

| `Op` | Predicate | Value |
|---|---|---|
| `OpEq`, `OpNe` | `=`, `<>` | one value |
| `OpGt`, `OpGe`, `OpLt`, `OpLe` | `>`, `>=`, `<`, `<=` | one value |
| `OpLike` | `LIKE` | one value |
| `OpIn` | `IN (...)` | a `[]any` |
| `OpIsNull`, `OpIsNotNull` | `IS NULL`, `IS NOT NULL` | ignored |

The page is an argument of the read, not a directive. `List(ctx, session, directives, page,
base...)` reads by offset: `Page` is a 1-based `Number` and a `Size`, both at least 1.
`Continue(ctx, session, directives, after, size, base...)` reads the `size` rows past `after`,
a `Cursor` a previous page returned; it refuses an empty cursor, since `List` reads a request's
first page. Both return a `Collection[T]`:

- `Items` is the page's rows.
- `Total` is the count under the filters, read from the same statement as the page, so it
  never disagrees with the page it came with. A continued page reports the same total a first
  page does. It is `NoTotal` (-1) when the request declined it, and on an empty page after the
  first or an empty continued page: no row carries the count, and a second statement could
  contradict the page. An empty first page reports 0.
- `More` reports whether a further page exists. The read fetches one row past the page's size
  to find out, and never scans that row.
- `Next` is the cursor that continues past the page's last row. It is empty when `More` is
  false or the ordering cannot be continued by cursor.

`base` is the base statement's own parameters, as `Args` merged left to right, a later value
winning; a parameter no argument names is an `ArgumentError`. `query.With("org", id)` builds
one.

The library composes the read from its own patterns: the base as a derived table `q`, the
predicates on `q.<field>`, the sort terms with the key appended as the tie-breaker, and the
paging clause. Each value binds through `CAST(placeholder AS <declared type>)`, so the engine
parses request text and a value it cannot read is the request's fault. Under `TotalExact` the
read adds `COUNT(*) OVER ()` in an inner layer over the filtered base and applies the keyset
predicate, the order, and the paging in an outer layer, so the count covers every row the
filters keep, not only those past a cursor. The count is a trailing column, `sqlate_total`, that
the scan never sees; a base declares no field of that name, and `Project` panics on one. A scan
that returns without calling `Scan` is an error, since the page's total goes unread. The window
holds the filtered rows before paging, where the plan under `TotalNone` can stop early along an
index, so walking a large collection by cursor declines the total after reading it once. The
composed text
depends only on the signature, the directives with the values removed, so the driver's
prepared-statement cache serves repeated requests.

The key makes the ordering total. After the caller's sorts, the read appends every key field
the sorts do not name, in header order, so a composite key tie-breaks in the order its header
declares. The keyed prefix is the shortest run of sort terms, from the first, that includes
every key field: those terms order the rows uniquely, and a cursor records the last row's
values for them. An appended key field takes the keyed prefix's direction.

An ordering is cursorable when every term of its keyed prefix sorts in one direction and every
field in it is declared `not null`. A cursorable read issues `Next`, and `Continue` adds the
keyset predicate, the rows past the cursor's values in that direction, to the filters. The
standard spelling of the predicate is a chain of disjuncts, `(a > x) OR (a = x AND b > y)`; an
engine may overlay it as a row-value comparison, `(a, b) > (x, y)`. A cursor is opaque,
URL-safe text a caller relays unchanged. It records the base, the keyed fields, the direction,
and the values, under a hash of those and each keyed field's declared type, so a cursor from
before a contract change is refused. It also records the filters, so `Continue` under other
filters than the page that issued it is refused as a `CursorMismatch`. Filters with a value
that has no JSON form, such as a float NaN, cannot be recorded, and their page reports `More`
without a `Next`.

Every error a request's declarations can cause unwraps to `ErrDirectives`, so a caller's
check for a bad request is one `errors.Is`:

- `UnknownFieldError`, with `Field` and `Use`, sort or filter.
- `UnknownOperatorError`.
- `InvalidValueError`, which names the field for a value of the wrong shape for its operator,
  or wraps the engine's error (and `sqlate.ErrInvalidValue`) for a value the engine rejected.
- `CursorError`, whose `Reason` says why `Continue` refused a cursor: `CursorMalformed`, a
  cursor that does not decode or verify; `CursorMismatch`, a cursor issued for another base,
  keyed fields, direction, or filters; `CursorUnsupported`, a sort that is not cursorable.
- A page number or size below 1, an empty cursor, or an unknown `TotalMode`.

### Verification

`Statements.Verify(ctx, session)` prepares every statement, and each returning command's
single-statement form beside it, so a reference the schema no longer satisfies fails at startup
with the statement named; a single-statement form's failure adds `(returning)` to the name.
`Projection.Verify` prepares a probe that names every declared field over the base and compares
each with a cast of its declared type, so a field the base no longer outputs, a declared type
the engine does not know, or a type that no longer matches its column fails the same way. A
second probe prepares one page past a cursor over the key, so the keyset predicate and the
paging clause, an engine's overlay of either included, are checked at startup too. A third
prepares the same page with its total, the window count beneath the keyset and the paging.
`query.Verify(ctx, session, verifiers...)` runs any number of them and joins their failures;
startup and any later check call it with the same arguments.

## migrate: schema versioning

**The migrations.** A `Migration` is one schema step: `Version`, `Name`, `Up` and `Down` (the
SQL texts; `Down` may be empty), and `Transactional`. `Files(fsys, dir)` reads the
`NNNN_name.up.sql` and `NNNN_name.down.sql` layout into a version-ordered list. The up file's
header decides `Transactional`; a down file that declares differently is an error. Versions must
be unique; a down without its up is an error; an up without its down is allowed.

**The sets.** A `Set` is one layer of a schema, as a library ships it or a program declares it:
`Name`, `Table` (its history table), and `Migrations`. A migrator runs one or more sets,
declared bottom-first: a set's migrations may reference the objects of the sets declared before
it, and never those of the sets after it. An empty `Table` is `DefaultTable`, `schema_version`,
so at most one set may leave it empty. A program that ran a single list of migrations under an
earlier release adopts sets with its history unchanged, since the default table and its columns
are the same.

```go
m, err := migrate.New(db, []migrate.Set{
	{Name: "audit", Table: "audit_schema_version", Migrations: auditMigrations},
	{Name: "app", Migrations: appMigrations},
}, migrate.Options{})
```

**The migrator.** `New(db, sets, options)` validates the sets (at least one, names and history
tables distinct, and in each set versions positive and strictly increasing, names and up texts
present) and takes the lock capability and the `Catalog` from the dialect when it has them. It
performs no I/O. `Options` has defaults for every field: `LockName` (`migrate.<table>`, over the
top set's table), `Unlocked`, and `Logger`.

A method that names no set acts on the top set, the last one declared, so a migrator over one
set acts on that set. `Up`, `Verify`, `Reset`, and `Status` cover every set.

| Method | Sets | Effect |
|---|---|---|
| `Up(ctx)` | every | Applies every pending migration, sets in declared order. |
| `Down(ctx, n)` | top | Reverts the n most recently applied; a migration without down text is `ErrNoDown`. |
| `Steps(ctx, n)` | top | Applies the next n when positive, reverts the last -n when negative; fewer remaining is not an error. |
| `Force(ctx, version)` | top | Sets the history to the version as an operator override, clearing a dirty row; nothing runs against the schema. |
| `Reset(ctx)` | every | Reverts every set in reverse declared order and drops each set's history table once that set is reverted, so a later `Up` replays every set from zero. |
| `Verify(ctx)` | every | Checks, without the lock, that each history is a clean, complete prefix of its set, and returns the first fault. |
| `Status(ctx)` | every | Reads, without the lock, a `SetStatus` per set: `Name`, `Table`, `Version` (the highest applied), `Latest`, `Pending`, and `Dirty`. |
| `Version(ctx)` | top | Reads the history's head: the highest applied version and whether it is dirty. |
| `Migrations()` | top | Returns a copy of the set's migrations. |

**Layers.** `Layers()` returns a `Layer` handle on every set in declared order, and
`Layer(name)` returns the one named, reporting whether it exists. A `Layer` has `Name`, `Table`,
`Migrations`, `Version`, `Verify`, `Status`, `Steps`, `Down`, and `Force`, each acting on that
set alone. The ordering between sets holds for every verb that runs migrations: a revert is
refused with `ErrAboveApplied` while a set above has applied migrations, and an apply is refused
with `ErrBelowPending` while a set below has pending ones. The top set's `Down` and `Steps` on
the migrator follow the same rule.

**The lock.** A run pins one connection, takes the dialect's named lock on it, does its work,
and releases the lock, so concurrent starters of the same program, in one process or across
processes, apply the sets once. A dialect without `Locker` fails with `ErrNoLocker` unless
`Options.Unlocked` opts out, in which case concurrent starters are unsafe. Inside the lock,
every run that applies or reverts migrations creates and reads every set's history table first,
so a dirty set or a history that does not match its set refuses the run before any migration
runs. `Force` is the exception, since it repairs a dirty history.

**Transactions and dirty state.** A transactional migration runs inside a transaction and
leaves nothing behind on failure. A migration headed `transaction: none` runs under autocommit
on the pinned connection, for DDL an engine refuses inside a transaction; its history row is
marked dirty before it runs and clean after. When it fails midway the row stays dirty, every
run refuses with a `DirtyError` (naming the version and, for the run that caused it, the
failure), `Verify` reports `ErrDirty`, and the repair is an operator's task: fix the schema,
`Force` the previous version, run again.

**The history table.** Standard DML with bound parameters, except the two statements whose
syntax differs between engines: creating the table and checking that it exists. `Catalog` is
that pair; a dialect provides it by implementing the two methods, and a dialect that does not
gets `StandardCatalog`, which serves MySQL and MariaDB. Its existence check matches the table
name in every schema, so the `postgres` dialect implements `Catalog` itself.

**Errors.** `ErrNoLocker`, `ErrDirty` (`DirtyError`), `ErrPending` (`PendingError`, the
unapplied versions), `ErrUnknownVersion` (`UnknownVersionError`, an applied row the set does
not contain), `ErrNoDown`, `ErrVersionNotFound`, `ErrAboveApplied`, and `ErrBelowPending`. An
error from one set's history or migrations is a `*SetError` whose `Set` names the set; it
unwraps to the set's own error, so `errors.Is` and `errors.As` reach the error inside it.

## sqltest: the scripted driver

`Open(t, responses...)` returns a `*sql.DB` over a fresh `Recorder`, closed when the test
ends. Each `Response` scripts the next exec or query call in order: an error, or the affected
count for an exec, or the columns and rows for a query, every row as wide as `Columns` and
made of `driver.Value` types as a real driver returns them. Prepare, begin, commit, and rollback
consume no responses; their failures are set on the recorder (`FailPrepare`, `FailBegin`,
`FailCommit`, `FailRollback`, `FailPing`).

The recorder is the test's evidence: `Calls()` returns every call (`Op`, `SQL`, `Args`
unconverted, `TxOptions`), `Ops()` the sequence of operations, `SQL(op)` the texts of one
kind, `Pending()` the responses not yet consumed, and `RowsLeaked()` the row sets never closed.
`Queue` appends responses mid-test.

The driver is strict where a real one is, so a test cannot pass on a path production would
reject: an unscripted call fails (`ErrUnscripted`), an argument count that does not match the
statement's `$N` placeholders fails (`ErrArguments`), and a response that does not fit its call
fails (`ErrScript`). It supports prepare, so `Verify` has a harness.

`Dialect` is the stub dialect: `$N` placeholders and a `MapError` that wraps every error in a
`*MappedError`, so a test proves with one `errors.As` that an error passed through the
mapping. `ReturningDialect` embeds it and implements `query.Returner` by appending `RETURNING`,
so a unit suite covers a returning command's single-statement form as well as its fallback,
which `Dialect` runs. `WithTotal(response, n)` adds the collection read's trailing count to a
scripted query's rows, so a consumer's suite scripts a counted page without naming the column.

## sqlint: the conventions linter

### Configuration

`sqlint.toml` lives at the module root, one file per module. `Load(fsys)` reads it; a missing
file is the defaults: every directory named `statements`, `patterns`, or `migrations` is its
role's, every check is on, and no source is registered.

```toml
# The engine whose export names the native forms, and the pattern sources.
engine = "github.com/standards-lab/sqlate/postgres"

[sources]
sql = "github.com/standards-lab/sqlate"
app = "patterns"
# A source with an engine overlay:
# sql = { path = "github.com/standards-lab/sqlate", overlay = "github.com/example/sqlate-mysql" }

# One table per role: the directory globs it covers and the switches of its checks.
[statements]
dirs = ["internal/*/statements"]
verb_named = true
delimiter = true
native_forms = true
guard = true

# A directory set that needs an exception overrides the role's switches under its glob.
[statements."internal/legacy/statements"]
native_forms = false

[patterns]
dirs = ["patterns"]
delimiter = true
native_forms = true

[migrations]
dirs = ["migrations"]
single_statement = true

# What this module declares to a consumer that names it as a source or an engine.
[export]
patterns = "patterns"
# overlay = "overlay"
# [export.native_forms]
# returning = '(?i)\bRETURNING\b'
```

A glob is slash-separated; each segment is a `path.Match` pattern and `**` matches any run of
segments. A source or the engine is a path: a directory of the tree, or a module path (its
first segment contains a dot). A producer, a module or a directory that contains its own
`sqlint.toml`, declares in `[export]` what a consumer reads: the directory its patterns
publish, the overlay directory an engine supplies, and the native forms an engine names. A
bare directory is the pattern files themselves. An engine is always a producer. The `sql`
namespace always resolves to the patterns the library embeds: the linter does not read the path
of a `sql` entry, and applies the entry's overlay, when one is declared, to those patterns.

Native forms are a deny list: each is a regular expression under the name a finding reports, so
the engine states in what position a spelling counts, word boundaries and case, and not only
which spelling. The linter matches them against code only, with string literals, quoted identifiers,
and comments stripped first. The PostgreSQL module exports `returning`, `on_conflict`, `ilike`,
`concurrently`, `limit`, `serial`, `jsonb`, `timestamptz`, `cast` (the `::` operator),
`pg_catalog` (any `pg_` function), `now`, and `uuidv7`.

### The checks

| Role | Check | Switch |
|---|---|---|
| statements | The directory compiles against the configured sources, the way a program compiles it: the header grammar, the parameter syntax, the field contract, includes resolving. | always |
| statements | A file is named for its operation, not its SQL verb (`insert_`, `select_`, `update_`, `upsert_`, `merge_`). | `verb_named` |
| statements, patterns | `{{` appears in no comment and no string literal. | `delimiter` |
| statements, patterns | A standard-tier file matches none of the engine's native forms. | `native_forms` |
| statements | A statement that includes one of the guard protocol's patterns includes both: a statement with a `SET` list that includes `guard_where` also includes `guard_set`, and a statement that includes `guard_set` also includes `guard_where`. The guard's check and a guarded delete include `guard_where` alone. The include is matched by pattern name under any namespace. | `guard` |
| patterns | The directory validates as a catalog source: a tier on every file, a port on every native file, parameters only. | always |
| migrations | A file headed `transaction: none` contains exactly one statement. | `single_statement` |

### The package and the command

`Lint(fsys, cfg, resolver)` walks the filesystem under the configuration and returns every
`Finding`: `Path`, `Line` (0 when the check has no line), and `Message`; `Finding.String()`
renders `path:line: message`. A nil configuration is the defaults. The `Resolver` turns a
module path into the filesystem of the module's root; `GoList(root)` resolves through
`go list -m` in the module at `root`, to the version its `go.mod` pins, a workspace or replace
directive included. A source or engine that does not resolve is a finding against
`sqlint.toml`, and the rest of the tree is linted with what did. Errors in the file's own
syntax are `Load`'s, one line each.

`cmd/sqlint` takes one argument, the module root (the working directory by default), loads,
lints with `GoList`, prints the findings sorted, and exits 1 on any. A harness calls the
package rather than the command.

## postgres: the PostgreSQL dialect

`postgres.Dialect{}` is the dialect: `Name` is `postgres`, `Placeholder(n)` is `$n`, and
`MapError` reads the SQLSTATE of pgx's error. A program opens its own pool with pgx's
`database/sql` driver and passes both to `sqlate.Wrap`.

| SQLSTATE | Result |
|---|---|
| class 22 | `sqlate.ErrInvalidValue` wrapping the driver error |
| 23505 | `ConstraintError` with `ErrUniqueViolation` |
| 23503 | `ConstraintError` with `ErrForeignKeyViolation` |
| 23514 | `ConstraintError` with `ErrCheckViolation` |
| 23502 | `ConstraintError` with `ErrNotNullViolation` |
| 2BP01 | `sqlate.ErrDependentObjects` wrapping the driver error |
| 40001 | `sqlate.ErrSerializationFailure` wrapping the driver error |
| anything else | the error unchanged, `sql.ErrNoRows` included |

Each `ConstraintError` carries the constraint, table, and column names the server reports.

`Lock` and `Unlock` implement `sqlate.Locker` over `pg_advisory_lock` and `pg_advisory_unlock`
on a pinned connection. The name enters the engine's key space through `hashtext`, so locks are
named and never numbered; the lock belongs to the connection's session and outlives any
transaction on it. `Unlock` of a lock the session does not hold is `ErrLockNotHeld`.

`CreateHistory` and `HistoryExists` implement `migrate.Catalog`. `CreateHistory` is
`StandardCatalog`'s DDL unchanged. `HistoryExists` checks for the table in the session's
current schema, so a table of the same name in another schema does not satisfy it.
`ServerVersion` returns `SELECT version()`, the statement an administrative read runs to report
the engine's version.

`postgres.Patterns()` is the library's patterns with one overlaid: the keyset predicate a
cursor continues by, spelled as the row-value comparison `(a, b) > (x, y)` in place of the
standard chain of disjuncts. The engine accepts every other library pattern as written. A
program passes `postgres.Patterns()` to `NewCatalog` in place of `query.Patterns()`, and only
the text of a continued page's predicate changes.

`Returning` implements `query.Returner` by appending `RETURNING` and the read's columns to an
`INSERT` or `UPDATE`. On PostgreSQL a returning command therefore runs as one statement, and the
read runs as a second only when the command changed nothing.

The module's `sqlint.toml` exports the engine's native forms and the overlay directory. Its
integration tier, behind the `integration` build tag, is the proofs only an engine can give:
non-transactional DDL, dirty state and repair, concurrent starters in one process and across
processes, the cancelled context, request values parsed by the engine, and both forms of a
returning command returning the same row. `mise run acceptance` runs them against a compose
PostgreSQL and tears it down.
