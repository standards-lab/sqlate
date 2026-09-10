# Concepts

sqlate has two kinds of `.sql` file, statements and patterns, and one grammar they share. This
document shows each construct by example. The [glossary](glossary.md) defines the terms; the
[features](features.md) document states every rule.

## A `.sql` file

A file is a header of `--|` declarations followed by a body. The library reads the header; the
engine receives the body only.

```sql
--| tier: standard
--| key: id
--| field: id uuid
--| field: code text
--| field: name text
--| field: version bigint
--| field: created_at timestamp
-- The read model. The SELECT list is the scan order.
SELECT id, code, name, version, created_at, updated_at
FROM team
```

`tier` is required on every file. `key` and `field` make this file a projection base, which a
collection read wraps as a query; they are explained below. A plain `--` comment in the header is prose,
skipped. The header ends at the first line that is neither blank, a comment, nor a declaration.

## Parameters

A parameter is `{{name}}`. Arguments bind by name, and the library renders each parameter as
the engine's placeholder in order of appearance.

```sql
--| tier: standard
SELECT version FROM team WHERE id = {{id}}
```

Compiled for PostgreSQL, the engine receives:

```sql
SELECT version FROM team WHERE id = $1
```

Written `{{name:type}}`, a parameter binds through `CAST`, so the engine parses the value as
the declared type and rejects one it cannot read:

```sql
--| tier: standard
INSERT INTO organization (parent_id, code, name)
VALUES ({{parent_id:uuid}}, {{code}}, {{name}})
```

```sql
INSERT INTO organization (parent_id, code, name)
VALUES (CAST($1 AS uuid), $2, $3)
```

Written `{{name...}}`, a parameter expands to one placeholder per element of a non-empty slice,
so an `IN` list binds as values and never as text:

```sql
--| tier: standard
SELECT id, code, name, version, created_at, updated_at
FROM team
WHERE code IN ({{codes...}})
ORDER BY code
```

Bound with two codes, the engine receives:

```sql
SELECT id, code, name, version, created_at, updated_at
FROM team
WHERE code IN ($1, $2)
ORDER BY code
```

## Patterns and includes

A pattern is a `.sql` file that shares SQL every statement would otherwise repeat. It is
published under a namespace, and a statement includes it with `{{> namespace.name}}`; the
library splices the pattern's text into the statement at compile time. An application's own
pattern, published under the namespace `app`:

```sql
--| tier: native
--| native: postgres, RETURNING. Ports: OUTPUT INSERTED (SQL Server), a second read (MySQL).
-- The identity every command returns: the row's key and its version.
RETURNING id, version
```

The statement that includes it:

```sql
--| tier: native
--| native: postgres, RETURNING through app.identity.
INSERT INTO team (code, name)
VALUES ({{code}}, {{name}})
{{> app.identity}}
```

The library publishes its own patterns under the namespace `sql`. Two of them are the
optimistic-concurrency guard: `guard_where` is `id = {{id}} AND version = {{version}}`, and
`guard_set` is `updated_at = CURRENT_TIMESTAMP, version = version + 1`. A guarded command
includes both:

```sql
--| tier: standard
UPDATE team
SET name = {{name}}, {{> sql.guard_set}}
WHERE {{> sql.guard_where}}
```

A pattern's parameters pass through the include and compile as parameters of the including
statement, so the statement above binds `name`, `id`, and `version`. A pattern never includes
another pattern.

## Sources, namespaces, and the catalog

A source is one namespace's patterns: a directory of `.sql` files. The catalog is the set of
registered sources, read and validated once where the program starts, and every statement
compiles against it.

```go
//go:embed statements/*.sql patterns/*.sql
var files embed.FS

catalog := query.MustCatalog(query.Patterns(), query.Publish("app", files, "patterns"))
stmts := catalog.MustCompile(files, "statements", db.Dialect())
```

`query.Patterns()` is the library's source under `sql`. `Publish` declares an application's
directory under the namespace it chooses. Two sources cannot share a namespace; when they would,
`As` registers one under an alias, the way an import is aliased:

```go
query.Publish("app", files, "patterns").As("shared")
```

## Tiers and the native declaration

Every file declares its tier. A standard file uses only standard SQL and runs on any engine. A
native file uses one engine's own feature, and its `native` declaration names the feature and
the port, the way the same effect is reached on other engines:

```sql
--| tier: native
--| native: postgres, pg_advisory_xact_lock over hashtext. Ports: sp_getapplock (SQL Server), GET_LOCK (MySQL).
--| transaction: required
SELECT pg_advisory_xact_lock(hashtext({{name}}))
```

`transaction: required` makes the statement refuse to run outside a transaction. The port list
of a whole code base is one search for `--| tier: native`, and the linter refuses a standard file
that uses a form the engine declares native.

## Overlays

An engine respells a library pattern by publishing a file of the same name with the same
parameters. The standard paging pattern is `OFFSET {{offset}} ROWS FETCH NEXT {{fetch}} ROWS
ONLY`; an engine without that form overlays it:

```sql
--| tier: native
--| native: mysql, LIMIT and OFFSET.
 LIMIT {{fetch}} OFFSET {{offset}}
```

```go
query.Patterns().Overlay(mysqlPatterns, ".")
```

An overlay can only respell what the source defines: a file naming no pattern of the source, or
declaring different parameters, is a catalog error. The library's patterns are written in
standard SQL, and PostgreSQL accepts every one of them as written, so the `postgres` sub-module
supplies no overlay.

## The projection base and the collection read

A projection base declares its `key`, the identity column and the sort tie-breaker, and one
`field` per column a request may filter or sort by, with the SQL type the request's text is cast
to. The declarations are an allow list: a request naming any other field is rejected before any
SQL exists.

At request time the library composes the collection read from its own patterns, with the base as
a derived table. A request for page 2 of 10, sorted by code then newest first, filtered by name
and by a set of codes, becomes:

```sql
SELECT * FROM (
  SELECT id, code, name, version, created_at, updated_at
  FROM team
) q
WHERE q.name LIKE CAST($1 AS text) AND q.code IN (CAST($2 AS text), CAST($3 AS text))
ORDER BY q.code, q.created_at DESC, q.id
OFFSET $4 ROWS FETCH NEXT $5 ROWS ONLY
```

Request values never enter as text: each is bound through its field's declared type, so a value
the engine cannot read as that type is a rejected request, not a server error. The count under
the same filters is the read's twin, and the single-row read is the base under one equality
predicate.

## Migrations

A migration is a `.sql` file too, in the `NNNN_name.up.sql` and `NNNN_name.down.sql` layout.
Its header has one optional declaration. `transaction: none` runs the migration outside a
transaction, for DDL an engine refuses inside one, and such a file contains exactly one
statement:

```sql
--| transaction: none
CREATE INDEX CONCURRENTLY team_code_ix ON team (code)
```

## The reserved delimiter

`{{` means a parameter or an include wherever it appears in a body, inside string literals and
comments included, so the body needs no lexer. A `{{` that forms neither is a load error, and
the linter reports it at the line.
