# Features

Every feature, package by package, in the detail needed to use it without reading the source.
The package documentation (`go doc`) is the reference for each name's exact signature.

The packages form two layers. The root package is the session layer every statement runs
through. `query` and `migrate` are the two capabilities built over it: authored statements, and
schema versioning that replaces a third-party migration dependency. `header` is the grammar
both read, `sqltest` is the driver their tests run over, and the three sub-modules are the
engine, an extension of the engine, and the linter.

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

**Transactions.** `DB.Begin(ctx, opts)` opens a `*Tx` with the standard library's
`*sql.TxOptions`: nil is a default transaction, and a non-nil value's `Isolation` and `ReadOnly`
reach the driver, as in `&sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true}`.
`Tx.Commit` maps its error, the one place a violation deferred to `COMMIT` can be classified;
`Tx.Rollback` does not. `DB.Transact(ctx, fn, opts)` is the runner, taking the same trailing
options: it begins, calls `fn(tx)`, commits on success, and returns `fn`'s result. On
`fn`'s error it rolls back and returns that error with a rollback failure joined onto it. A
panic in `fn` rolls back and re-panics, so no transaction leaks to the pool. `Beginner` is the
interface of a session that can open a transaction. Its `Begin` has `DB.Begin`'s signature, and
`*DB` and any type embedding it satisfy it. A protocol handed a session rather than a
transaction asserts `Beginner` to open its own. `sqlate.Transact(ctx, b, fn, opts)` is the same
runner over any `Beginner`, so a protocol, or an application's own pool type embedding `*DB`,
runs its unit exactly as `DB.Transact` does; `DB.Transact` is `Transact` over the `*DB`.

**Pinned connections.** `DB.Conn(ctx)` pins one connection for a protocol that needs session
scope: a session-level lock, or DDL an engine refuses inside a transaction. The caller closes
it.

**Dialect.** `Dialect` is what the library needs from an engine: `Name`, `Placeholder(n)`, the
engine's syntax for the nth bind parameter, and `MapError`, the classification of a driver
error into the library's sentinels. A sub-module named for the engine implements it. `Locker`
is a capability a dialect may add: `Lock` and `Unlock` of a named session-level lock on a
pinned connection; `migrate` asserts it. `TryLocker` embeds `Locker` and adds `TryLock`, which
tries the same lock without waiting and reports whether it took it: false, and no error, while
another session holds the name; a protocol that skips its work rather than wait asserts it,
and `migrate` polls it, when the dialect has it, to wait without holding a statement open.
`query.Returner` is another: it renders a returning command as one statement, and `Compile`
asserts it.

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
| `field` | projection bases | One per column a request may filter or sort by: `<name> <sql type>`, with an optional trailing `not null`, matched case-insensitively, for a column that never holds a null; a misspelled suffix that leaves `not` or `null` in the type is a load error. |

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
| `Guarded(check, version)` | `Guard` | `Run(ctx, session, version, args)` binds the expected version under the named parameter, runs the command, and returns the new version; when the command changed nothing it runs the check: no row is `sql.ErrNoRows`, a row at another version is `ErrVersionMismatch` wrapping the expected and current versions, and a row at the expected version, which a predicate of the command's own refused, is `ErrRefused`. |
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
the field name lowercased; `db:"-"` excludes it. A base aliases its output columns to the
contract's names, so the `json` tag is the usual source and `db` the override.

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
- Times come back in UTC. Every `time.Time` that `Scanner` and `Scalar` return is in
  `time.UTC`, whatever `time.Local` is and whatever location the driver gave it. pgx,
  for one, delivers a `timestamp with time zone` in `time.Local`. This covers a `time.Time`,
  a `*time.Time`, and a valid `sql.NullTime` or `sql.Null[time.Time]`, including one reached
  through an embedded struct. Only the location changes: the instant is the same, and a zero
  time stays zero. A cursor carries a keyed time as UTC text, so the same row issues the same
  cursor on every host. A hand-written `ScanFunc` gets the driver's values as they are.
- A `ScanFunc[T]` reads its row through `Row`, an interface of the two methods a scan needs:
  `Columns` and `Scan`. `*sql.Rows` satisfies it, and so does the adapter through which the
  collection read hides its total column from the scan. A scan therefore relies on nothing
  beyond `Row` and reads the row with one `Scan`.
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
- `Total` is the count under the filters. The page's own statement reads it, so it never
  disagrees with the page. A continued page reports the same total a first page does. `Total`
  is `NoTotal` (-1) when the request declined the count, and on an empty page after the first
  or an empty continued page, since no row carries the count and a second statement could
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
parses request text and a value it cannot read is the request's fault. The composed text
depends only on the signature, the directives with the values removed, so the driver's
prepared-statement cache serves repeated requests.

Under `TotalExact` the read adds `COUNT(*) OVER ()` in an inner layer over the filtered base,
and applies the keyset predicate, the order, and the paging in an outer layer, so the count
covers every row the filters keep, not only those past a cursor. The count arrives as a
trailing column, `sqlate_total`, which the scan never sees; a base may declare no field of that
name, in any case, and `Project` panics on one. A base that outputs an undeclared column of that
name is refused when the page is read, as is a page whose last column is not the count. A scan
that returns without calling `Scan` is an error, since the page's total would go unread, and a
scan given the wrong number of destinations is told the count of the columns it sees. The window
reads every filtered row before paging, where the plan under `TotalNone` can stop early along an
index, so a caller walking a large collection by cursor reads the total once and declines it
after.

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
before a contract change is refused. The hash is unkeyed and guards against accident, not
forgery: a cursor's values re-enter the read only as bound parameters cast to the fields' types,
so a forged cursor can at most select another page. A cursor also records the filters, so
`Continue` under other filters than the page that issued it is refused as a `CursorMismatch`.
Filters with a value that has no JSON form, such as a float NaN, cannot be recorded, and their
page reports `More` without a `Next`.

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
probe prepares the same page counted, with the window count beneath the keyset predicate and
the paging.
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
present), validates the extension declarations, and takes the lock capability, the `Catalog`,
and the `ExtensionCatalog` from the dialect when it has them. It performs no I/O. `Options` has
defaults for every field: `LockName` (`migrate.<table>`, over the top set's table), `Unlocked`,
`Logger`, and `Extensions`.

A method that names no set acts on the top set, the last one declared, so a migrator over one
set acts on that set. `Up`, `Verify`, `Reset`, and `Status` cover every set.

| Method | Sets | Effect |
|---|---|---|
| `Up(ctx)` | every | Activates the declared extensions, applies every pending migration, sets in declared order, then removes the extensions declared removed. |
| `Down(ctx, n)` | top | Reverts the n most recently applied; a migration without down text is `ErrNoDown`. |
| `Steps(ctx, n)` | top | Applies the next n when positive, after activating the declared extensions as `Up` does but removing none, reverts the last -n when negative; fewer remaining is not an error. |
| `Force(ctx, version)` | top | Sets the history to the version as an operator override: every migration through it recorded clean, those above it removed, a dirty row cleared; nothing runs against the schema. |
| `Reset(ctx)` | every | Reverts every set in reverse declared order and drops each set's history table once that set is reverted, so a later `Up` replays every set from zero; extensions stay installed. |
| `Verify(ctx)` | every | Checks, without the lock, every extension declaration and then that each history is a clean, complete prefix of its set, and returns the first fault. |
| `Status(ctx)` | every | Reads, without the lock, a `SetStatus` per set: `Name`, `Table`, `Version` (the highest applied), `Latest`, `Pending`, and `Dirty`. |
| `Version(ctx)` | top | Reads the history's head: the highest applied version and whether it is dirty. |
| `Extensions(ctx)` | none | Reads, without the lock, an `ExtensionStatus` per extension declaration, in declared order. |
| `Migrations()` | top | Returns a copy of the set's migrations. |

**Layers.** `Layers()` returns a `Layer` handle on every set in declared order, and
`Layer(name)` returns the one named, reporting whether it exists. A `Layer` has `Name`, `Table`,
`Migrations`, `Version`, `Verify`, `Status`, `Steps`, `Down`, and `Force`, each acting on that
set alone; `Layer.Verify` and `Layer.Status` read no extension. The ordering between sets holds
for every verb that runs migrations: a revert is refused with `ErrAboveApplied` while a set above
has applied migrations, and an apply is refused with `ErrBelowPending` while a set below has
pending ones. The top set's `Down` and `Steps` on the migrator follow the same rule.

**The lock.** A run pins one connection, takes the dialect's named lock on it, does its work,
and releases the lock, so concurrent starters of the same program, in one process or across
processes, apply the sets once. A dialect with `TryLocker` is polled while another session holds
the lock: each refused `TryLock` pauses, from 25ms doubling to a 1s cap, and tries again, and
the wait ends with the context's error when the context ends, holding nothing. No statement
stays open while a starter waits, so a holder's `CREATE INDEX CONCURRENTLY`, which waits for
every open transaction, cannot deadlock with the waiter. A dialect with `Locker` alone waits in
its blocking `Lock`. The lock is released under a context that survives the run's
cancellation. Once the run's context has ended, an unlock failure is not reported, because the
discarded connection's session ends and releases the lock. A dialect without `Locker` fails with
`ErrNoLocker` unless `Options.Unlocked` opts out, in which case concurrent starters are unsafe.
Inside the lock, every run that applies or reverts migrations creates and reads every set's
history table first, so a dirty set or a history that does not match its set refuses the run
before any migration runs. `Force` is the exception, since it repairs a dirty history.

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
name in every schema, so the `postgres` dialect implements `Catalog` itself. The history's
`applied_at` records an instant: `timestamp with time zone` on PostgreSQL, and `timestamp` in
`StandardCatalog`, which MySQL and MariaDB store as an instant. A dialect can also implement
`HistoryUpgrader` to bring a history table from an earlier release up to date. Each locked run
then checks the table after creating it, before reading it, and alters it in place only when
the check reports it outdated, so a current table is never rewritten.

**Extensions.** `Options.Extensions` declares, in order, the database extensions the migrator
manages. An `Extension` has a `Name` and a `Removed` flag and no version: the version is the
default the database image offers. `New` refuses an empty or repeated name, and refuses any
declaration when the dialect lacks the `ExtensionCatalog` capability. That capability returns
the engine's statements as text, as `Catalog` does, and quotes the name itself:

- `ExtensionState(param)` returns the query that reads one extension, its name bound at `param`, as
  one row of three columns: the installed version (NULL when not installed), the default version
  the database offers (NULL when it does not offer the extension), and whether an update path
  leads from the installed version to the default. No row reads as neither installed nor offered.
- `ActivateExtension(name)` returns the statements that create the extension when it does not exist
  and then update it to its default.
- `RemoveExtension(name)` returns the statement that drops the extension when it exists, with
  `RESTRICT`.

`Extensions(ctx)` reads each declaration's state on the pool, without the lock. Each
`ExtensionStatus` has `Name`, `Removed`, `Installed` (empty when not installed), `Default` (empty
when the database does not offer the extension), and `Pending`. An active declaration is pending
while its extension is missing but offered, or installed behind the default with an update path to
it; an extension the database does not offer, or one with no update path, is not pending. A
removed declaration is pending while it is still installed. An extension the database does not
offer is not an error.

`Up` and a positive `Steps`, on the migrator or on a layer, activate the declarations. The stage
runs inside the run's lock, on its pinned connection, or without the lock under `Unlocked`, as
the sets do. It comes after every set's history is checked and after a layer's ordering refusal,
and before any migration runs. It reads every active declaration first, skipping removed ones,
and refuses the run, if any fails, before activating any. An extension the database does not
offer fails with an `*ExtensionError` over `ErrExtensionUnavailable`. An installed version that
differs from the default with no update path to it, which covers one newer than the default,
fails with an `*ExtensionError` over a `*NoUpdatePathError` naming both versions. Once every
declaration passes, a declaration already at the default runs no statement, so a pre-installed
database passes under a role that cannot create extensions. Each missing or older one runs
`ActivateExtension`'s statements in its own transaction, in declared order, and is logged
through `Options.Logger` as `extension activated` with its name, the default version, and the
installed version it came from. A failed activation stops the run with an `*ExtensionError` over
the mapped driver error, leaving the activations before it in place and no set applied. `Down`,
a negative `Steps`, `Reset`, and `Force` do not run the stage.

`Up` alone removes. After every set is applied, it reads each removed declaration in declared
order, on the same pinned connection and inside the same lock, or without the lock under
`Unlocked`. Each one still installed runs `RemoveExtension`'s statement in its own transaction
and is logged as `extension removed` with its name and the installed version. A removed
declaration whose extension is not installed, whether or not the database offers it, runs no
statement. Because the drop is `RESTRICT`, an object still depending on the extension, such as a
column of its type, fails the run with an `*ExtensionError` naming the extension, over the
driver's error mapped through the dialect; the sets stay applied, the drops before it stay in
place, and no later declaration is dropped. A set migration that drops the dependent object lets
the next `Up` remove the extension. `Steps`, positive or negative, `Down`, `Reset`, and `Force`
never remove. Deleting a declaration only stops the migrator from managing the extension: it is
never dropped, and an extension no declaration names runs no statement.

`Verify` checks the declarations before any set, on the pool and without the lock, in declared
order, and returns the first fault as an `*ExtensionError` naming the extension. An active
declaration missing but offered, or installed behind its default with an update path, and a
removed declaration still installed, are over an `*ExtensionPendingError`, so they unwrap to
`ErrPending`. One the database does not offer is over `ErrExtensionUnavailable`, and an installed
version with no update path to the default is over a `*NoUpdatePathError`; neither is
`ErrPending`, since `Up` would refuse them rather than resolve them. A declaration at its default,
and a removed one not installed, pass. Once every declaration passes, `Verify` checks the sets.
These verdicts match `Up`'s checks and `Extensions`' `Pending`. The migrator's `Status` reads no
extension.

**Errors.** `ErrNoLocker`, `ErrDirty` (`DirtyError`), `ErrPending` (`PendingError`, the
unapplied versions), `ErrUnknownVersion` (`UnknownVersionError`, an applied row the set does
not contain), `ErrNoDown`, `ErrVersionNotFound`, `ErrAboveApplied`, and `ErrBelowPending`. An
error from one set's history or migrations is a `*SetError` whose `Set` names the set; it
unwraps to the set's own error, so `errors.Is` and `errors.As` reach the error inside it. An
error from one extension declaration is an `*ExtensionError` whose `Extension` names the
extension, over its class: `ErrExtensionUnavailable`, an extension the database does not offer;
`ErrNoUpdatePath` (`NoUpdatePathError`, naming the installed and default versions), an installed
version with no update path to the default, which covers one newer than the default; or
`ErrPending` (`ExtensionPendingError`), a declaration the database does not match yet.

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
which `Dialect` runs. `LockingDialect` embeds it and implements `sqlate.TryLocker`, and so
`sqlate.Locker`: `Lock`, `TryLock`, and `Unlock` each run one statement on the pinned connection
(`SELECT lock($1)`, `SELECT try_lock($1)`, `SELECT unlock($1)`), so a test scripts their
outcomes like any other call; an unlock the script answers false is `ErrLockNotHeld`.
`WithTotal(response, n)` adds the collection read's trailing count
column, holding `n`, to a scripted response, so a consumer's suite scripts a counted page
without naming the column.

## sqlint: the conventions linter

### Configuration

`sqlint.toml` lives at the module root, one file per module. `Load(fsys)` reads it; a missing
file is the defaults: every directory named `statements`, `patterns`, or `migrations` is its
role's, every check is on, and no source is registered.

```toml
# The engine whose export names the native forms, and the pattern sources.
engine = "github.com/standards-lab/sqlate/postgres"
# Modules whose exported native forms join the engine's; listing any requires an engine.
# extensions = ["github.com/standards-lab/sqlate/postgres/pgvector"]

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
# An extension module that adds no syntax declares the table empty and says why:
# reason = "the extension adds functions, not syntax"
# native_forms = {}
# [export.native_forms]
# returning = '(?i)\bRETURNING\b'
```

A glob is slash-separated; each segment is a `path.Match` pattern and `**` matches any run of
segments. A source, the engine, or an extension is a path: a directory of the tree, or a module
path (its first segment contains a dot). A producer, a module or a directory that contains its own
`sqlint.toml`, declares in `[export]` what a consumer reads: the directory its patterns publish,
the overlay directory an engine supplies, and the native forms an engine or an extension names. A
bare directory is the pattern files themselves. An engine is always a producer. The `sql`
namespace always resolves to the patterns the library embeds: the linter does not read the path
of a `sql` entry, and applies the entry's overlay, when one is declared, to those patterns.

Native forms are a deny list: each is a regular expression under the name a finding reports, so
the engine states in what position a spelling counts, word boundaries and case, and not only
which spelling. The linter matches them against code only, with string literals, quoted identifiers,
and comments stripped first. The PostgreSQL module exports `returning`, `on_conflict`, `ilike`,
`concurrently`, `limit`, `serial`, `jsonb`, `timestamptz`, `cast` (the `::` operator),
`pg_catalog` (any `pg_` function), `now`, and `uuidv7`.

`extensions` lists the modules whose native forms join the engine's. An extension module
exports its own forms in `[export.native_forms]`, or declares the table empty, `native_forms = {}`,
beside a `reason`, a string saying why it names none; a `reason` may also accompany forms.
`Load` refuses an `extensions` that is not a list of strings, an empty entry, an entry listed
twice, and any `extensions` while `engine` is unset, one line each.

`Lint` finds each entry the way it finds the engine: a module path through the `Resolver`, and
anything else as a directory of the tree. It accepts a module whose export names at least one
native form, or declares the table empty beside a non-empty `reason`. `Lint` reports each of these
failures against `sqlint.toml` and lints the rest of the tree with what resolved:

- an entry that does not resolve, or has no `sqlint.toml`: `extensions: <entry>: ` and the error;
- a refused module, one with no `native_forms` table or an empty table and no reason:
  `extensions: <entry> exports neither native forms nor a reason`; it adds no forms;
- a form whose expression does not compile: `extensions: <entry>: native_forms.<name>: ` and the
  error; the module's other forms still apply.

An accepted module's forms are matched exactly where the engine's are, and a finding from one names
the entry beside the form:
`"<=>" (cosine_distance, from github.com/standards-lab/sqlate/postgres/pgvector) in a standard-tier
file; declare the tier native and name the port`. A name the engine and an extension
both export is matched and reported for each. A native file that uses an extension's syntax names
the extension in its free-text `native:` header, `native: postgres, pgvector — <=> cosine distance`;
the linter does not read that header, so review checks it. The linter cannot read Go values either,
so review keeps the `extensions` list in step with `migrate.Options.Extensions`.

### The checks

| Role | Check | Switch |
|---|---|---|
| statements | The directory compiles against the configured sources, the way a program compiles it: the header grammar, the parameter syntax, the field contract, includes resolving. | always |
| statements | A file is named for its operation, not its SQL verb (`insert_`, `select_`, `update_`, `upsert_`, `merge_`). | `verb_named` |
| statements, patterns | `{{` appears in no comment and no string literal. | `delimiter` |
| statements, patterns | A standard-tier file matches none of the native forms of the engine and the accepted extensions. | `native_forms` |
| statements | A statement that includes one of the guard protocol's patterns includes both: a statement with a `SET` list that includes `guard_where` also includes `guard_set`, and a statement that includes `guard_set` also includes `guard_where`. The guard's check and a guarded delete include `guard_where` alone. The include is matched by pattern name under any namespace. | `guard` |
| patterns | The directory validates as a catalog source: a tier on every file, a port on every native file, parameters only. | always |
| migrations | A file headed `transaction: none` contains exactly one statement. | `single_statement` |

### The package and the command

`Lint(fsys, cfg, resolver)` walks the filesystem under the configuration and returns every
`Finding`: `Path`, `Line` (0 when the check has no line), and `Message`; `Finding.String()`
renders `path:line: message`. A nil configuration is the defaults. The `Resolver` turns a
module path into the filesystem of the module's root; `GoList(root)` resolves through
`go list -m` in the module at `root`, to the version its `go.mod` pins, a workspace or replace
directive included. A source, engine, or extension that does not resolve is a finding against
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
PostgreSQL 19 records a `NOT NULL` as a named constraint in `pg_constraint`, yet its not-null
violation still reports no constraint name, so `Column` remains that error's handle.

`Lock` and `Unlock` implement `sqlate.Locker` over `pg_advisory_lock` and `pg_advisory_unlock`
on a pinned connection. The name enters the engine's key space through `hashtext`, so locks are
named and never numbered; the lock belongs to the connection's session and outlives any
transaction on it. `Unlock` of a lock the session does not hold is `ErrLockNotHeld`.
`TryLock` implements `sqlate.TryLocker` over `pg_try_advisory_lock` in the same key space: it
takes the lock without waiting and reports false, with no error, while another session holds the
name through `Lock` or `TryLock`. `Unlock` releases a lock it takes, as does the end of the
holding connection's session.

`CreateHistory` and `HistoryExists` implement `migrate.Catalog`. `CreateHistory` is
`StandardCatalog`'s DDL with `applied_at` as `timestamp with time zone`. `HistoryExists` checks
for the table in the session's current schema, so a table of the same name in another schema
does not satisfy it. `HistoryOutdated` and `UpgradeHistory` implement `migrate.HistoryUpgrader`.
A history table created before v0.5.0, whose `applied_at` is a `timestamp` without time zone,
is altered on the next `Up`, `Steps`, `Down`, `Reset`, or `Force`. The column becomes
`timestamp with time zone`, and each stored wall clock is read as UTC.
`ServerVersion` returns `SELECT version()`, the statement an administrative read runs to report
the engine's version.

`ExtensionState`, `ActivateExtension`, and `RemoveExtension` implement `migrate.ExtensionCatalog`,
so a migrator under `postgres.Dialect{}` accepts extension declarations. `ExtensionState` reads
`pg_available_extensions` for the name bound at `$1`: its `installed_version` and
`default_version`, and an update-path flag, true when `pg_extension_update_paths` lists a path
from the installed version to the default. The flag is irrelevant once the installed version is
the default, and false when nothing is installed. An extension the server does not offer
yields no row. `ActivateExtension` is `CREATE EXTENSION IF NOT EXISTS` and then
`ALTER EXTENSION ... UPDATE`, so a missing extension is created at its default version and an
older one is updated to it. `RemoveExtension` is `DROP EXTENSION IF EXISTS ... RESTRICT`, whose
refusal while an object depends on the extension is SQLSTATE 2BP01, `sqlate.ErrDependentObjects`
through `MapError`. Each statement quotes the name as an identifier.

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
returning command returning the same row. For extensions, they prove that an extension installed
behind its default is pending until `Up` updates it, that an extension the image lacks refuses
`Up` before any set, that a removal waits for a set migration to drop the column depending on
it, that `Reset` leaves an extension installed, and that two concurrent `Up` runs activate an
extension once. For the total, they prove that a counted page agrees with its total when writes
commit between the statement and the caller, where a count read as its own statement does not;
that it agrees under concurrent writers; and that the counted page scans its base once under one
window while the uncounted page keeps the key's index order. `mise run acceptance` runs them
against a compose PostgreSQL and tears it down.

### pgtest: a test's throwaway database

`pgtest` gives an integration test its own PostgreSQL database, so no test depends on the
state of a shared database or on another test. `Create(t, env, prefix)` creates
`<prefix>_test_<hex>` on the server whose admin DSN the environment variable `env` holds, and
fails the test when `env` is unset. It returns a `*Database` holding a pinged pool over the new
database (`Pool`) and the admin DSN pointed at it (`DSN`), which a test hands to a process it
starts. `Migrated(t, env, prefix, sets...)` is `Create` with the `migrate.Set`s applied in order
under the PostgreSQL dialect, and `Database.Session()` wraps the pool under `Dialect`.
`MigratedWith(t, env, prefix, opts, sets...)` is `Migrated` under the caller's
`migrate.Options`, so a test can declare extensions, name the lock, or take the migrator's log;
`Migrated` is `MigratedWith` under empty options.

When the test ends the pool is closed and the database dropped `WITH (FORCE)`, so a session
still open on it, such as a started process's pool, is ended rather than holding the drop back;
the drop is retried while the server reports a session still closing. A create that fails is
followed by a drop too, since a create that timed out can still finish on the server. The
package imports `testing`, so only test files import it.

## postgres/pgvector: the pgvector extension

`github.com/standards-lab/sqlate/postgres/pgvector` names what a program needs to know of
pgvector, PostgreSQL's vector similarity extension. It imports only the standard library, so a
module that builds queries over pgvector takes its names without taking a driver. It is tagged
`postgres/pgvector/vX.Y.Z`.

`pgvector.Extension` is the extension's catalog name, `vector`: the name an application declares
in `migrate.Options.Extensions` and `CREATE EXTENSION` installs. `pgvector.Metrics()` returns
the metric table of the `vector` type, one `Metric` per distance metric, each with its distance
operator and the operator class an HNSW or IVFFlat index builds for it:

| `Metric` | `Name` | `Operator` | `OpClass` |
|---|---|---|---|
| `L2` | `l2` | `<->` | `vector_l2_ops` |
| `Cosine` | `cosine` | `<=>` | `vector_cosine_ops` |
| `InnerProduct` | `inner product` | `<#>` | `vector_ip_ops` |

The table covers `vector` only; `halfvec`, `sparsevec`, `bit`, and the L1 metric are left out
until a space needs them.

The module's `sqlint.toml` exports pgvector's native forms, for a consumer that lists the module
under sqlint's `extensions`. Each form is named for pgvector's own name for what it matches:

- the distance operators, named for their functions: `l2_distance` (`<->`),
  `negative_inner_product` (`<#>`), `cosine_distance` (`<=>`), `l1_distance` (`<+>`),
  `hamming_distance` (`<~>`), and `jaccard_distance` (`<%>`);
- `vector` where a type goes, a cast's `AS vector` before `)` or `[`, and `vector(` with a
  dimension; `halfvec` and `sparsevec` anywhere as words;
- `hnsw` and `ivfflat`, the index methods after `USING`;
- the functions, named with `()` so they stay apart from the operators that share their names:
  `l2_distance()`, `inner_product()`, `cosine_distance()`, `l1_distance()`,
  `hamming_distance()`, `jaccard_distance()`, `vector_dims()`, `vector_norm()`, `l2_norm()`,
  `l2_normalize()`, `binary_quantize()`, `subvector()`, `array_to_vector()`,
  `array_to_halfvec()`, and `array_to_sparsevec()`, each matched as a call;
- `hnsw.*` and `ivfflat.*`, the extension's settings.

A column, table, or alias named `vector` matches no form, and neither does a portable statement.
The engine's `cast` form already denies `::vector`.

sqlate's own `sqlint.toml` lists the module under `extensions` although no `Options.Extensions`
in the repository declares pgvector. The listing is a test: the root check resolves the module
through the committed `go.work`, so the check fails when sqlint refuses the module's export or
a form does not compile.

### A space per dimension on one column

An application that stores embeddings of several models, each with its own dimension, keeps them
in one untyped `vector` column and a `space` column naming the model. Each space gets a partial
HNSW index over the column cast to its dimension, with the operator class of its metric:

```sql
CREATE TABLE item (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    space integer NOT NULL,
    embedding vector NOT NULL
);

CREATE INDEX item_space1_l2 ON item
    USING hnsw ((embedding::vector(3)) vector_l2_ops) WHERE space = 1;
CREATE INDEX item_space2_cosine ON item
    USING hnsw ((embedding::vector(4)) vector_cosine_ops) WHERE space = 2;
```

A query uses a space's index only when it repeats both the cast and the space predicate, ordering
by the cast column with the metric's operator under a `LIMIT`:

```sql
SELECT id FROM item WHERE space = 1 ORDER BY embedding::vector(3) <-> $1 LIMIT 5;
```

Without the cast, `ORDER BY embedding <-> $1` does not match the index's expression, and the
engine scans the space's rows instead. The cast also checks the dimension: an insert into an
indexed space computes the index's expression, so a vector of the wrong dimension fails with
`expected 3 dimensions` and the row is not written. A space with no index accepts any dimension.

The operator and the operator class come from the module's metric table, so a program builds
both statements from a `Metric` rather than spelling them.

### An image with pgvector

The official PostgreSQL image does not ship pgvector, so sqlate's compose image builds it from
source. `compose/postgres/Dockerfile` is the canonical Dockerfile: a consumer that declares
pgvector copies it to its own `compose/postgres/Dockerfile` and keeps its pins current.

```dockerfile
FROM postgres:19.0-alpine

ARG PGVECTOR_VERSION=0.8.7

ADD https://github.com/pgvector/pgvector.git#v${PGVECTOR_VERSION} /usr/src/pgvector

RUN apk add --no-cache --virtual .build-deps build-base \
    && cd /usr/src/pgvector \
    && make with_llvm=no OPTFLAGS="" \
    && make install with_llvm=no \
    && cd / \
    && rm -rf /usr/src/pgvector \
    && apk del .build-deps

ENV POSTGRES_USER=app \
    POSTGRES_PASSWORD=app \
    POSTGRES_DB=app

HEALTHCHECK --start-period=30s --start-interval=1s --interval=5s --timeout=3s --retries=5 \
    CMD ["pg_isready", "-h", "127.0.0.1", "-U", "app", "-d", "app"]
```

The image has two pins. `FROM` pins PostgreSQL; `ARG PGVECTOR_VERSION` pins pgvector, and the
`ADD` line fetches pgvector's git repository at the tag `v${PGVECTOR_VERSION}`, so the `ARG` is
the extension's one version source. `pg_available_extensions` then offers `vector` at that
version, and `CREATE EXTENSION vector` installs it.

The build flags:

- `with_llvm=no`, on both `make` and `make install`, skips the LLVM bitcode for PostgreSQL's
  JIT. The image's PostgreSQL is built with LLVM support, so without the flag `make install`
  looks for the matching `clang`, which the Alpine build tools do not provide, and fails.
- `OPTFLAGS=""`, on `make`, drops pgvector's default `-march=native`, so the build does not tie
  the image to the CPU of the machine that built it.

The build tools are installed as a virtual package and removed, with the source, in the same
layer, so the image grows by only the extension, about 5 MB.

`mise run currency` reads both pins. It compares the `FROM` tag with the image's tags, and an
`ARG <NAME>_VERSION=<v>` whose `ADD` line fetches
`https://github.com/<owner>/<repo>.git#v${<NAME>_VERSION}` with that repository's highest
semver tag, read from the repository's tags since pgvector publishes no GitHub releases. A
trailing `ARG` is reported as `compose/postgres/Dockerfile: pgvector/pgvector 0.8.6 -> 0.8.7`
and fails currency; a lookup that fails fails it too. Other `ARG`s are not read. `mise run
upgrade` does not move the `ARG`; it is raised by hand.

### The integration tier

The module's integration tier, behind the `integration` build tag, runs against the compose
image through `SQLATE_DSN` and `pgtest`, with a throwaway database per test. It proves that a
declaration of `pgvector.Extension` in `migrate.Options.Extensions` installs `vector` at the
image's default version, that `Extensions` then reports it current and `Verify` passes, and that
a removal is refused while a `vector` column depends on the extension and succeeds once a set
migration drops the column. For the per-space recipe, with one space per metric of the table, it
proves that a query repeating the cast and the space predicate plans an index scan on its
space's index, that the same query without the cast uses none of them, and that an insert of the
wrong dimension into an indexed space fails. `mise run integration` runs this tier with the
`postgres` module's, and `mise run acceptance` starts the stack, runs both, and drops it.
