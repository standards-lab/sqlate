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
panic in `fn` rolls back and re-panics, so no transaction leaks to the pool.

**Pinned connections.** `DB.Conn(ctx)` pins one connection for a protocol that needs session
scope: a session-level lock, or DDL an engine refuses inside a transaction. The caller closes
it.

**Dialect.** `Dialect` is what the library needs from an engine: `Name`, `Placeholder(n)`, the
engine's syntax for the nth bind parameter, and `MapError`, the classification of a driver
error into the library's sentinels. A sub-module named for the engine implements it. `Locker`
is a capability a dialect may add: `Lock` and `Unlock` of a named session-level lock on a
pinned connection; `migrate` asserts it.

**Errors.** The root package defines the errors a consumer matches on, and every package keeps
its own sentinels and error types in one file, `errors.go`, so a package's whole taxonomy is
read in one place.

| Error | Meaning | Match with |
|---|---|---|
| `ErrConnectionFailed` | A connection or a transaction could not be obtained. Wraps the driver's error. | `errors.Is` |
| `ErrInvalidValue` | A data exception: a bound value the engine could not read as the type it was cast to (SQLSTATE class 22). Wraps the driver's error. | `errors.Is` |
| `ErrUniqueViolation`, `ErrForeignKeyViolation`, `ErrCheckViolation`, `ErrNotNullViolation` | The four constraint classes. | `errors.Is` on the class |
| `ConstraintError` | The value a dialect returns for a constraint violation: `Class` (one of the four), `Err` (the driver's error), and `Constraint`, the violated constraint's name when the driver exposes it. | `errors.As` |

`errors.As` finds the driver's error through every wrap. `sql.ErrNoRows` is never mapped.

## header: the declaration grammar

`header.Parse(text)` reads the header of a `.sql` file: the leading run of blank lines,
plain `--` comments (prose, skipped), and `--|` declaration lines of the form `--| key: value`.
The header ends at the first line that is none of those. A `--|` line that is not a
declaration is an error, and a declaration after the body has begun is an error. `Header.End()`
is the byte offset where the body begins; `Get`, `All`, and `Keys` read the declarations. The
package knows no keys; each consumer decides which it accepts.

The keys `query` and `migrate` accept:

| Key | Files | Meaning |
|---|---|---|
| `tier` | statements, patterns | Required: `standard` or `native`. |
| `native` | statements, patterns | Required when the tier is native: the engine feature used and the port, as free text. |
| `transaction` | statements | `required`: the statement refuses to run outside a transaction. |
| `transaction` | migrations | `none`: the migration runs outside a transaction; `required` or absent keeps one. |
| `key` | projection bases | The identity column and sort tie-breaker. |
| `field` | projection bases | One per column a request may filter or sort by: `<name> <sql type>`. |

## query: authored statements

### The catalog

`Publish(namespace, fsys, dir)` declares the `.sql` files under a directory as the patterns of
a namespace; nothing is read until the catalog is built. `Patterns()` is the library's own
source under the namespace `sql`. `Source.As(namespace)` registers a source under an alias,
and `Source.Overlay(fsys, dir)` replaces its patterns by name with an engine's own; a later
overlay wins over an earlier one.

`NewCatalog(sources...)` reads every source and validates it: each file declares a tier, a
native file names its port, a pattern includes no other pattern, an overlay respells only what
its source defines with the same parameters, and no two sources share a namespace. Every
failure is reported, joined, each naming the namespace and file. `MustCatalog` panics instead,
for the place a program starts. `Catalog.Namespaces()` and `Catalog.Patterns()` list the
inventory, so a program can report what it compiled against.

### Compilation

`Catalog.Compile(fsys, dir, dialect)` reads every `.sql` file under a directory, parses its
header, splices its includes from the catalog, and renders its parameters as the dialect's
placeholders in order of appearance. A file without a header, with an unknown declaration, with
a header the grammar rejects, or with an include the catalog cannot resolve is a load error
naming the file. `MustCompile` panics instead. The result is a `*Statements`, the directory's
inventory: `Statement(name)` returns the statement named by its file's base name and panics on
a missing one, `Statements()` lists them in name order, and `Verify` prepares each against a
session.

A `Statement` reports what its file declared: `Name`, `Text` (the body as the engine receives
it, less a trailing semicolon), `Tier`, `Native`, `TransactionRequired`, `Key`, `Fields`,
`Params` (the parameter names in position order), and `Catalog`, the catalog it compiled
against.

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
| `Project(scan)` | `Projection[T]` | `List(ctx, session, directives)` returns the page and the total count; `One(ctx, session, field, value)` is the base under one equality predicate; `Verify` probes the field contract. A base without a key or field contract, or one with parameters of its own, panics at binding. |
| `Guarded(check, version)` | `Guard` | `Run(ctx, session, version, args)` binds the expected version under the named parameter, runs the command, and returns the new version; when the command changed nothing it runs the check: no row is `sql.ErrNoRows`, a row is `ErrVersionMismatch` wrapping the expected and current versions. |

A statement headed `transaction: required` refuses to run against `*DB` with
`ErrTransactionRequired`.

### Struct-tag mapping

An entity's tags are its scan and binding contract, so a program writes neither scan functions
nor argument literals. A field's column name is its `db` tag, else its `json` tag's name, else
the field name lowercased; `db:"-"` excludes it.

- `Scanner[T]()` returns the `ScanFunc[T]` for `T`: each row's columns are matched to fields by
  name and scanned into a fresh `T`. A column `T` has no field for is an error, so a `SELECT`
  list that grows past its entity fails; a field with no column stays zero.
- `Scalar[T]` is the scan function for a single-column row.
- `ArgsOf(v)` binds a struct's fields as `Args` by their column names; a nil pointer binds
  `NULL`.
- `Args` is `map[string]any`. A missing name is an `ArgumentError`, a programming error rather
  than request input; an extra name is ignored, so one map serves a guard's command and its
  narrower check. `Args.With(name, value)` returns a copy with one more binding, for an input
  that arrives separately from the command's fields, such as the row's id.

### Directives and composition

`Directives` is one read request against a projection: `Page` (1-based `Number` and `Size`,
both at least 1), `Sort` (a list of `{Field, Descending}`), and `Filters` (a list of
`{Field, Op, Value}`). Field names reference the base's declared fields; an unknown name is
rejected as an `UnknownFieldError` before any SQL is composed.

| `Op` | Predicate | Value |
|---|---|---|
| `OpEq`, `OpNe` | `=`, `<>` | one value |
| `OpGt`, `OpGe`, `OpLt`, `OpLe` | `>`, `>=`, `<`, `<=` | one value |
| `OpLike` | `LIKE` | one value |
| `OpIn` | `IN (...)` | a `[]any` |
| `OpIsNull`, `OpIsNotNull` | `IS NULL`, `IS NOT NULL` | ignored |

The library composes the read from its own patterns: the base as a derived table `q`, the
predicates on `q.<field>`, the sort terms with the key appended as the tie-breaker, and the
paging clause. Each value binds through `CAST(placeholder AS <declared type>)`, so the engine
parses request text and a value it cannot read is the request's fault. The count under the same
filters runs first and is the read's total. The composed text depends only on the signature,
the directives with the values removed, so the driver's prepared-statement cache serves repeated
requests.

Every error a request's declarations can cause unwraps to `ErrDirectives`, so a caller's
check for a bad request is one `errors.Is`: `UnknownFieldError` (with `Field` and `Use`, sort or
filter), `UnknownOperatorError`, and `InvalidValueError`, which names the field for a value of
the wrong shape for its operator, or wraps the engine's error (and `sqlate.ErrInvalidValue`)
for a value the engine rejected.

### Verification

`Statements.Verify(ctx, session)` prepares every statement, so a reference the schema no
longer satisfies fails at startup with the statement named. `Projection.Verify` prepares a
probe naming every declared field and the key over the base, so a field the base no longer
outputs, or a declared type the engine does not know, fails the same way. `query.Verify(ctx,
session, verifiers...)` runs any number of them and joins their failures; startup and any
later check call it with the same arguments.

## migrate: schema versioning

**The set.** A `Migration` is one schema step: `Version`, `Name`, `Up` and `Down` (the SQL
texts; `Down` may be empty), and `Transactional`. `Files(fsys, dir)` reads the
`NNNN_name.up.sql` and `NNNN_name.down.sql` layout into a version-ordered set. The up file's
header decides `Transactional`; a down file that declares differently is an error. Versions must
be unique; a down without its up is an error; an up without its down is allowed.

**The migrator.** `New(db, set, options)` validates the set (versions positive and strictly
increasing, names and up texts present) and takes the lock capability and the `Catalog` from
the dialect when it has them. `Options` has defaults for every field: `Table` (the history
table, `schema_version`), `LockName` (`migrate.<table>`), `Unlocked`, and `Logger`.

| Method | Effect |
|---|---|
| `Up(ctx)` | Applies every pending migration. |
| `Down(ctx, n)` | Reverts the n most recently applied; a migration without down text is `ErrNoDown`. |
| `Steps(ctx, n)` | Applies the next n when positive, reverts the last -n when negative. |
| `Force(ctx, version)` | Sets the history to the version as an operator override, clearing a dirty row; nothing runs against the schema. |
| `Verify(ctx)` | Checks, without the lock, that the history is a clean, complete prefix of the set. |
| `Version(ctx)` | Reads the history's head: the highest applied version and whether it is dirty. |

**The lock.** A run pins one connection, takes the dialect's named lock on it, does its work,
and releases the lock, so concurrent starters of the same program, in one process or across
processes, apply the set once. A dialect without `Locker` fails with `ErrNoLocker` unless
`Options.Unlocked` opts out, in which case concurrent starters are unsafe.

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
gets `StandardCatalog`, which serves PostgreSQL, MySQL, and MariaDB.

**Errors.** `ErrNoLocker`, `ErrDirty` (`DirtyError`), `ErrPending` (`PendingError`, the
unapplied versions), `ErrUnknownVersion` (`UnknownVersionError`, an applied row the set does
not contain), `ErrNoDown`, and `ErrVersionNotFound`.

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
mapping.

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
bare directory is the pattern files themselves. An engine is always a producer.

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
| anything else | the error unchanged, `sql.ErrNoRows` included |

`Lock` and `Unlock` implement `sqlate.Locker` over `pg_advisory_lock` and `pg_advisory_unlock`
on a pinned connection. The name enters the engine's key space through `hashtext`, so locks are
named and never numbered; the lock belongs to the connection's session and outlives any
transaction on it. `Unlock` of a lock the session does not hold is `ErrLockNotHeld`.

The module's `sqlint.toml` exports the engine's native forms. It supplies no overlay, since
PostgreSQL accepts every library pattern as written in standard SQL. Its integration tier,
behind the `integration` build tag, is the proofs only an engine can give: non-transactional
DDL, dirty state and repair, concurrent starters in one process and across processes, the
cancelled context, and request values parsed by the engine. `mise run acceptance` runs them
against a compose PostgreSQL and tears it down.
