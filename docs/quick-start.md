# Quick start

A tutorial that builds a working program from an empty directory: a `teams` package with
validated commands and a store over a `team` table, a migration, a shared pattern, seed data,
a test that needs no database, the linter over the SQL, and a program whose startup and
runtime are split into layers. The library is used on its own, over a plain `*sql.DB`. Every
file is written in full at the step that needs it, and every block is taken from a program
that compiles, runs against PostgreSQL 18, and passes its tests. The [concepts](concepts.md)
document explains the grammar the files use, and the [features](features.md) document
explains each mechanism.

## Prerequisites

- [mise](https://mise.jdx.dev/getting-started.html) installs the Go toolchain the project pins
  and runs its tasks.
- [Docker](https://docs.docker.com/get-started/get-docker/) with the Compose plugin runs
  PostgreSQL. Any Docker Engine or Docker Desktop release from 2023 on includes it.

## 1. Create the module

```sh
mkdir teams && cd teams
go mod init example.com/teams
go get github.com/standards-lab/sqlate github.com/standards-lab/sqlate/postgres
go get github.com/jackc/pgx/v5
go get -tool github.com/standards-lab/sqlate/sqlint/cmd/sqlint
```

The base module has no dependencies. The `postgres` sub-module names the driver, so a program
imports it once, where it opens its pool. The `-tool` form records the linter's command in
`go.mod`, so `go tool sqlint` runs it at the pinned version.

`mise.toml` pins the toolchain, sets the connection string every task reads, and names the
tasks the tutorial runs:

`mise.toml`:

```toml
[tools]
go = "1.27"

[env]
SQLATE_DSN = "postgres://app:app@127.0.0.1:5433/app?sslmode=disable"

[tasks.db-up]
description = "Start PostgreSQL and wait until it is healthy"
run = "docker compose up -d --wait"

[tasks.db-down]
description = "Stop PostgreSQL and drop its volume"
run = "docker compose down -v"

[tasks.run]
description = "Run the program"
run = "go run ."

[tasks.test]
description = "Run the tests; no database needed"
run = "go test ./..."

[tasks.lint]
description = "Check the SQL files against the conventions"
run = "go tool sqlint ."
```

```sh
mise trust && mise install
```

## 2. Start PostgreSQL

`compose.yml` runs PostgreSQL 18 on port 5433 with the user, password, and database the
connection string names, and a health check the `db-up` task waits on.

`compose.yml`:

```yaml
services:
  postgres:
    image: postgres:18-alpine
    container_name: teams-postgres
    environment:
      POSTGRES_USER: app
      POSTGRES_PASSWORD: app
      POSTGRES_DB: app
    ports:
      - "127.0.0.1:5433:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U app -d app"]
      interval: 2s
      timeout: 3s
      retries: 15
```

```sh
mise run db-up
```

## 3. Write the migration

A migration is a pair of files, `NNNN_name.up.sql` and `NNNN_name.down.sql`. The `version` and
`updated_at` columns are the ones the library's guard patterns advance.

`migrations/0001_team.up.sql`:

```sql
CREATE TABLE team (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code text NOT NULL,
  name text NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT uq_team_code UNIQUE (code)
);
```

`migrations/0001_team.down.sql`:

```sql
DROP TABLE team;
```

## 4. Write a pattern

A pattern is SQL that several statements share, published under a namespace; the program's
patterns are published under `app` by the database layer of the next step. This one is the
identity every command returns, and it uses a PostgreSQL feature, so its tier is native and its
`native` declaration names the feature and the port to other engines.

`patterns/identity.sql`:

```sql
--| tier: native
--| native: postgres, RETURNING. Ports: OUTPUT INSERTED (SQL Server), a second read (MySQL).
-- The identity every command returns: the row's key and its version.
RETURNING id, version
```

## 5. Write the database and schema layers

The program's startup is a sequence of layers, each in its own file. The database layer opens
the pool with the driver, wraps it with the PostgreSQL dialect, and builds the catalog of
pattern namespaces every store compiles against: the library's under `sql`, the program's
under `app`.

`database.go`:

```go
package main

import (
	"database/sql"
	"embed"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"
)

//go:embed patterns/*.sql
var patterns embed.FS

// Database is the program's database layer: the session over the pool, and
// the catalog of pattern namespaces every store compiles against.
type Database struct {
	DB      *sqlate.DB
	Catalog *query.Catalog
	pool    *sql.DB
}

// openDatabase opens the pool with the driver, wraps it with the PostgreSQL
// dialect, and builds the catalog: the library's patterns under sql, the
// program's under app.
func openDatabase(dsn string) (*Database, error) {
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	catalog, err := query.NewCatalog(query.Patterns(), query.Publish("app", patterns, "patterns"))
	if err != nil {
		return nil, err
	}
	return &Database{DB: sqlate.Wrap(pool, postgres.Dialect{}), Catalog: catalog, pool: pool}, nil
}

// Close closes the pool.
func (d *Database) Close() error { return d.pool.Close() }
```

The schema layer reads the embedded migrations and applies them under the engine's advisory
lock, so several starters of the same program apply them once.

`schema.go`:

```go
package main

import (
	"context"
	"embed"

	"github.com/standards-lab/sqlate/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Schema is the program's schema layer: the migrator over the embedded set.
type Schema struct {
	migrator *migrate.Migrator
}

// newSchema reads the embedded migrations and builds the migrator.
func newSchema(db *Database) (*Schema, error) {
	set, err := migrate.Files(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	migrator, err := migrate.New(db.DB, set, migrate.Options{})
	if err != nil {
		return nil, err
	}
	return &Schema{migrator: migrator}, nil
}

// Up applies every pending migration under the engine's advisory lock and
// returns the version the schema is at.
func (s *Schema) Up(ctx context.Context) (int, error) {
	if err := s.migrator.Up(ctx); err != nil {
		return 0, err
	}
	head, err := s.migrator.Version(ctx)
	return head.Version, err
}

// Down reverts the most recently applied migration.
func (s *Schema) Down(ctx context.Context) error { return s.migrator.Down(ctx, 1) }
```

`main.go` runs the sequence so far: connect, migrate, report the version.

`main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
)

func main() {
	ctx := context.Background()

	db, err := openDatabase(os.Getenv("SQLATE_DSN"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	schema, err := newSchema(db)
	if err != nil {
		log.Fatal(err)
	}
	version, err := schema.Up(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("schema version", version)
}
```

```sh
mise run run
```

```
schema version 1
```

`Up` is idempotent; running the program again reports the same version.

## 6. Write the statements

The `teams` package owns its statements, one file per operation, under its own directory.
`create` includes the pattern under the namespace `app`.

`teams/statements/create.sql`:

```sql
--| tier: native
--| native: postgres, RETURNING through app.identity.
INSERT INTO team (code, name)
VALUES ({{code}}, {{name}})
{{> app.identity}}
```

`seed` inserts a team unless one with the code exists, in standard SQL, so seeding is
idempotent. The id is a parameter cast to `uuid`, so seed rows keep the ids the seed file
gives them.

`teams/statements/seed.sql`:

```sql
--| tier: standard
-- Inserts a team unless one with the code exists, so a seed is idempotent.
MERGE INTO team AS t
USING (VALUES ({{id:uuid}}, {{code}}, {{name}})) AS s (id, code, name)
ON t.code = s.code
WHEN NOT MATCHED THEN INSERT (id, code, name) VALUES (s.id, s.code, s.name)
```

`team_view` is the projection base the collection read wraps. Its `key` is the identity column
and its `field` declarations are the only names a request may filter or sort by, each with the
SQL type the request's value is cast to.

`teams/statements/team_view.sql`:

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

`version` is the guard's check: it reads a row's current version by key.

`teams/statements/version.sql`:

```sql
--| tier: standard
SELECT version FROM team WHERE id = {{id}}
```

`edit` and `delete` are guarded commands. Both include the library's guard patterns, published
under the namespace `sql`: `guard_where` is `id = {{id}} AND version = {{version}}`, and
`guard_set` advances `updated_at` and `version`.

`teams/statements/edit.sql`:

```sql
--| tier: standard
UPDATE team
SET name = {{name}}, {{> sql.guard_set}}
WHERE {{> sql.guard_where}}
```

`teams/statements/delete.sql`:

```sql
--| tier: standard
DELETE FROM team
WHERE {{> sql.guard_where}}
```

`find_by_codes` expands a list: `{{codes...}}` binds one placeholder per element of the slice
the program passes.

`teams/statements/find_by_codes.sql`:

```sql
--| tier: standard
SELECT id, code, name, version, created_at, updated_at
FROM team
WHERE code IN ({{codes...}})
ORDER BY code
```

## 7. Write the entities

The entities are the package's types: the read model, the commands and the seed row with their
validation, and the identity every command returns. Their struct tags are the scan and binding
contract, so the store writes no scan function and no argument literal. A command validates
what it can know from its own fields; what only the database knows, such as a duplicate code,
is the store's, as the engine's constraint violation.

`teams/entities.go`:

```go
package teams

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// ErrValidation classifies a command input the program rejects before any
// SQL runs; the wrapped reason names the field.
var ErrValidation = errors.New("teams: invalid command")

// codePattern is the schema's rule for a code, checked here so a bad code
// is a validation error rather than a database check violation.
var codePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Team is the read model; the tags name the columns Scanner reads.
type Team struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewTeam is the create command's input; ArgsOf binds its fields by name.
type NewTeam struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Validate rejects a malformed code or an empty name. Uniqueness of the code
// is the store's, as the engine's constraint violation.
func (t NewTeam) Validate() error {
	return errors.Join(validCode(t.Code), validName(t.Name))
}

// RenameTeam is the rename command's input.
type RenameTeam struct {
	Name string `json:"name"`
}

// Validate rejects an empty name.
func (r RenameTeam) Validate() error { return validName(r.Name) }

// SeedTeam is one row of reference data: a team with its id fixed, so a
// seed is the same on every database it runs against.
type SeedTeam struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// Validate rejects a malformed code or an empty name.
func (t SeedTeam) Validate() error {
	return errors.Join(validCode(t.Code), validName(t.Name))
}

// Identity is what every command returns: the row's key and the version
// the command left it at.
type Identity struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

func validCode(code string) error {
	if !codePattern.MatchString(code) {
		return fmt.Errorf("%w: code must be lowercase words joined by single hyphens", ErrValidation)
	}
	return nil
}

func validName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name must not be empty", ErrValidation)
	}
	return nil
}
```

## 8. Write the store

The store compiles the package's statements against the catalog and binds each one, once, to
the value that runs it: `Project` for the collection read, `Scan` for a statement that returns
rows, `Guarded` for a command with its check, and the bare `Statement` for one run with `Exec`.
Each operation takes the session, so it runs against the pool here and inside a transaction in
`Seed` and `Replace`.

`teams/database.go`:

```go
package teams

import (
	"context"
	"embed"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
)

//go:embed statements/*.sql
var statements embed.FS

// Store is the package's SQL client: each statement bound once to the value
// that runs it. The entities' tags are the scan and binding contract, so no
// scan function or argument literal is written here.
type Store struct {
	db      *sqlate.DB
	stmts   *query.Statements
	view    query.Projection[Team]
	create  query.Rows[Identity]
	byCodes query.Rows[Team]
	seed    query.Statement
	rename  query.Guard
	delete  query.Guard
}

// NewStore compiles the package's statements against the catalog and binds
// them.
func NewStore(db *sqlate.DB, catalog *query.Catalog) *Store {
	stmts := catalog.MustCompile(statements, "statements", db.Dialect())
	check := stmts.Statement("version")
	return &Store{
		db:      db,
		stmts:   stmts,
		view:    stmts.Statement("team_view").Project(query.Scanner[Team]()),
		create:  stmts.Statement("create").Scan(query.Scanner[Identity]()),
		byCodes: stmts.Statement("find_by_codes").Scan(query.Scanner[Team]()),
		seed:    stmts.Statement("seed"),
		rename:  stmts.Statement("edit").Guarded(check, "version"),
		delete:  stmts.Statement("delete").Guarded(check, "version"),
	}
}

// Verify prepares every statement and the view's field contract against
// the live schema, so a drifted schema fails at startup.
func (s *Store) Verify(ctx context.Context) error {
	return query.Verify(ctx, s.db, s.stmts, s.view)
}

// Seed inserts every row whose code is not yet present, as one unit of work,
// and returns how many it inserted.
func (s *Store) Seed(ctx context.Context, rows []SeedTeam) (int64, error) {
	for _, r := range rows {
		if err := r.Validate(); err != nil {
			return 0, err
		}
	}
	return s.db.Transact(ctx, func(tx *sqlate.Tx) (int64, error) {
		var inserted int64
		for _, r := range rows {
			n, err := s.seed.Exec(ctx, tx, query.ArgsOf(r))
			if err != nil {
				return 0, err
			}
			inserted += n
		}
		return inserted, nil
	})
}

// Create validates the command and runs it; the result is the new row's
// identity.
func (s *Store) Create(ctx context.Context, t NewTeam) (Identity, error) {
	if err := t.Validate(); err != nil {
		return Identity{}, err
	}
	return s.create.One(ctx, s.db, query.ArgsOf(t))
}

// List runs the collection read under the request's directives and returns
// the page and the total count.
func (s *Store) List(ctx context.Context, d query.Directives) ([]Team, int, error) {
	return s.view.List(ctx, s.db, d)
}

// Find returns the team with the code; no row is sql.ErrNoRows.
func (s *Store) Find(ctx context.Context, code string) (Team, error) {
	return s.view.One(ctx, s.db, "code", code)
}

// FindByCodes returns the teams with any of the codes, in code order.
func (s *Store) FindByCodes(ctx context.Context, codes []string) ([]Team, error) {
	return s.byCodes.All(ctx, s.db, query.Args{"codes": codes})
}

// Rename runs the guarded command with the version the caller read and
// returns the new version.
func (s *Store) Rename(ctx context.Context, id string, version int64, r RenameTeam) (int64, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	return s.rename.Run(ctx, s.db, version, query.ArgsOf(r).With("id", id))
}

// Replace deletes one team and creates another as one unit of work.
func (s *Store) Replace(ctx context.Context, id string, version int64, t NewTeam) (Identity, error) {
	if err := t.Validate(); err != nil {
		return Identity{}, err
	}
	return s.db.Transact(ctx, func(tx *sqlate.Tx) (Identity, error) {
		if _, err := s.delete.Run(ctx, tx, version, query.Args{"id": id}); err != nil {
			return Identity{}, err
		}
		return s.create.One(ctx, tx, query.ArgsOf(t))
	})
}
```

- `Verify` prepares every statement and probes every declared field against the live schema,
  so a renamed column fails at startup with the statement named.
- `Seed` validates every row, then runs the seed statement for each inside one transaction;
  `Exec` returns the rows affected, so the count is how many were new.
- `Create` validates, then runs the command with `One`, which returns the first row; `ArgsOf`
  binds the command's fields by their tag names.
- `List` runs the collection read under the request's directives and returns the page and the
  total count. `Find` is the base under one equality predicate; no row is `sql.ErrNoRows`.
- `FindByCodes` runs the expanded statement with `All`, which returns every row. `Each` yields
  rows one at a time as an iterator.
- `Rename` validates, then runs the guarded command with the version the caller read and
  returns the new one; `With` adds the id the command needs beside the command's own fields.
- `Replace` runs two commands as one unit of work: `Transact` commits on success and rolls back
  on error or panic.

## 9. Test without a database

`sqltest` is a scripted `database/sql` driver: each response is consumed by the next call in
order, and every call is recorded. Its stub dialect renders `$n` placeholders, so the compiled
text is the one PostgreSQL receives. The test publishes the `app` namespace itself, from an
in-memory file system, so the package's tests depend on nothing outside the package.

`teams/patterns_test.go`:

```go
package teams_test

import "testing/fstest"

// patterns is the app namespace as the test publishes it: the one pattern
// the statements include, so the test needs no file outside the package.
var patterns = fstest.MapFS{
	"identity.sql": {Data: []byte("--| tier: native\n--| native: postgres, RETURNING.\nRETURNING id, version")},
}
```

`teams/database_test.go`:

```go
package teams_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"example.com/teams/teams"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

func store(t *testing.T, responses ...sqltest.Response) (*teams.Store, *sqltest.Recorder) {
	t.Helper()
	pool, rec := sqltest.Open(t, responses...)
	catalog := query.MustCatalog(query.Patterns(), query.Publish("app", patterns, "."))
	return teams.NewStore(sqlate.Wrap(pool, sqltest.Dialect{}), catalog), rec
}

func TestCreate_BindsTheCommandAndScansTheIdentity(t *testing.T) {
	s, rec := store(t, sqltest.Response{
		Columns: []string{"id", "version"},
		Rows:    [][]driver.Value{{"7d0f", int64(1)}},
	})

	id, err := s.Create(context.Background(), teams.NewTeam{Code: "core", Name: "Core"})
	if err != nil || id.ID != "7d0f" || id.Version != 1 {
		t.Fatalf("Create = %+v, %v", id, err)
	}
	call := rec.Calls()[0]
	if call.SQL != "INSERT INTO team (code, name)\nVALUES ($1, $2)\nRETURNING id, version" {
		t.Errorf("sql = %q", call.SQL)
	}
	if call.Args[0] != "core" || call.Args[1] != "Core" {
		t.Errorf("args = %v", call.Args)
	}
	if rec.Pending() != 0 {
		t.Error("a scripted response went unconsumed")
	}
}

func TestCreate_RejectsABadCodeBeforeAnySQL(t *testing.T) {
	s, rec := store(t)
	_, err := s.Create(context.Background(), teams.NewTeam{Code: "Not Valid", Name: "x"})
	if !errors.Is(err, teams.ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if len(rec.Calls()) != 0 {
		t.Error("the rejected command was sent to the driver")
	}
}
```

```sh
mise run test
```

```
ok  	example.com/teams/teams
```

The driver is strict where a real one is: an unscripted call, a wrong argument count, and a
response of the wrong shape each fail the test. The second test proves a rejected command
never reaches the driver.

## 10. Lint the SQL

`sqlint.toml` at the module root names the pattern sources and the engine by module path, and
the directory of each role.

`sqlint.toml`:

```toml
engine = "github.com/standards-lab/sqlate/postgres"

[sources]
sql = "github.com/standards-lab/sqlate"
app = "patterns"

[statements]
dirs = ["teams/statements"]

[patterns]
dirs = ["patterns"]

[migrations]
dirs = ["migrations"]
```

```sh
mise run lint
```

```
sqlint: ok
```

The linter compiles the statement directory against the same catalog the program builds,
validates the pattern directory, and checks the conventions: a file named for its operation,
the delimiter kept out of comments and literals, no native form in a standard-tier file, and
one statement per non-transactional migration. A finding prints as `path:line: message` and
exits 1.

## 11. Write the seed, the store layer, the work, and the program

The seed data is reference data every database starts with: three teams with fixed ids.

`seeds/teams.json`:

```json
[
  {"id": "0192b2a0-0000-7000-8000-000000000001", "code": "core", "name": "Core"},
  {"id": "0192b2a0-0000-7000-8000-000000000002", "code": "edge", "name": "Edge"},
  {"id": "0192b2a0-0000-7000-8000-000000000003", "code": "data", "name": "Data"}
]
```

The seed layer decodes the embedded file and inserts the rows through the store. Its statement
is idempotent, so seeding runs at every startup.

`seed.go`:

```go
package main

import (
	"context"
	_ "embed"
	"encoding/json"

	"example.com/teams/teams"
)

//go:embed seeds/teams.json
var teamSeeds []byte

// seed is the program's seed layer: the reference data every database
// starts with, decoded from the embedded files and inserted through the
// stores. Each store's seed statement is idempotent, so seeding runs at
// every startup.
func seed(ctx context.Context, stores *Stores) (int64, error) {
	var rows []teams.SeedTeam
	if err := json.Unmarshal(teamSeeds, &rows); err != nil {
		return 0, err
	}
	return stores.Teams.Seed(ctx, rows)
}
```

The store layer builds every package store over the database and verifies them together.

`stores.go`:

```go
package main

import (
	"context"

	"example.com/teams/teams"
)

// Stores is the program's store layer: one store per package, each compiled
// against the shared catalog.
type Stores struct {
	Teams *teams.Store
}

// newStores builds every store over the database.
func newStores(db *Database) *Stores {
	return &Stores{Teams: teams.NewStore(db.DB, db.Catalog)}
}

// Verify checks every store against the live schema.
func (s *Stores) Verify(ctx context.Context) error {
	return s.Teams.Verify(ctx)
}
```

The work is the program's runtime: the reads, each command, and the errors a caller matches
on. The list reads print as a table and the single reads as JSON.

`run.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"example.com/teams/teams"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
)

// run is the program's work: the reads as JSON, then the commands, then the
// errors a caller matches on.
func run(ctx context.Context, stores *Stores) error {
	page, total, err := stores.Teams.List(ctx, query.Directives{
		Page: query.Page{Number: 1, Size: 10},
		Sort: []query.Sort{{Field: "code"}},
	})
	if err != nil {
		return err
	}
	fmt.Println("list:", len(page), "of", total)
	printTable(page)

	one, err := stores.Teams.Find(ctx, "core")
	if err != nil {
		return err
	}
	fmt.Println("find:")
	print(one)

	some, err := stores.Teams.FindByCodes(ctx, []string{"core", "edge"})
	if err != nil {
		return err
	}
	fmt.Println("find by codes:")
	printTable(some)

	created, err := stores.Teams.Create(ctx, teams.NewTeam{Code: "platform", Name: "Platform"})
	if err != nil {
		return err
	}
	fmt.Println("create:")
	print(created)

	version, err := stores.Teams.Rename(ctx, created.ID, created.Version, teams.RenameTeam{Name: "Platform Engineering"})
	if err != nil {
		return err
	}
	fmt.Println("rename: version", version)

	replaced, err := stores.Teams.Replace(ctx, created.ID, version, teams.NewTeam{Code: "infra", Name: "Infrastructure"})
	if err != nil {
		return err
	}
	fmt.Println("replace:")
	print(replaced)

	// The errors a caller matches on.
	_, err = stores.Teams.Create(ctx, teams.NewTeam{Code: "Not Valid", Name: "x"})
	fmt.Println("invalid command:", errors.Is(err, teams.ErrValidation))

	_, err = stores.Teams.Create(ctx, teams.NewTeam{Code: "core", Name: "Again"})
	var violation *sqlate.ConstraintError
	if errors.Is(err, sqlate.ErrUniqueViolation) && errors.As(err, &violation) {
		fmt.Println("duplicate:", violation.Constraint)
	}

	_, _, err = stores.Teams.List(ctx, query.Directives{
		Page:    query.Page{Number: 1, Size: 10},
		Filters: []query.Filter{{Field: "id", Op: query.OpEq, Value: "not-a-uuid"}},
	})
	fmt.Println("bad request:", errors.Is(err, query.ErrDirectives))

	_, err = stores.Teams.Rename(ctx, one.ID, one.Version-1, teams.RenameTeam{Name: "Stale"})
	fmt.Println("stale version:", errors.Is(err, query.ErrVersionMismatch))
	return nil
}

// print writes v as indented JSON.
func print(v any) {
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(out))
}

// printTable writes the teams as a table.
func printTable(rows []teams.Team) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CODE\tNAME\tVERSION\tID")
	for _, t := range rows {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", t.Code, t.Name, t.Version, t.ID)
	}
	w.Flush()
}
```

`main.go` now runs the whole sequence: connect, migrate, build the stores, verify them, seed,
do the work, revert the migration, and close.

`main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
)

// main is the program's sequence: connect, migrate, build the stores, verify
// them, seed, do the work, revert the migration, and close.
func main() {
	ctx := context.Background()

	db, err := openDatabase(os.Getenv("SQLATE_DSN"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	schema, err := newSchema(db)
	if err != nil {
		log.Fatal(err)
	}
	version, err := schema.Up(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("schema version", version)

	stores := newStores(db)
	if err := stores.Verify(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("stores verified")
	seeded, err := seed(ctx, stores)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("seeded", seeded, "teams")

	if err := run(ctx, stores); err != nil {
		log.Fatal(err)
	}

	if err := schema.Down(ctx); err != nil {
		log.Fatal(err)
	}
}
```

```sh
mise run run
```

```
schema version 1
stores verified
seeded 3 teams
list: 3 of 3
CODE  NAME  VERSION  ID
core  Core  1        0192b2a0-0000-7000-8000-000000000001
data  Data  1        0192b2a0-0000-7000-8000-000000000003
edge  Edge  1        0192b2a0-0000-7000-8000-000000000002
find:
{
  "id": "0192b2a0-0000-7000-8000-000000000001",
  "code": "core",
  "name": "Core",
  "version": 1,
  "created_at": "2026-09-04T18:30:27.637066Z",
  "updated_at": "2026-09-04T18:30:27.637066Z"
}
find by codes:
CODE  NAME  VERSION  ID
core  Core  1        0192b2a0-0000-7000-8000-000000000001
edge  Edge  1        0192b2a0-0000-7000-8000-000000000002
create:
{
  "id": "6b57bc47-cbc5-4e41-8520-fb5de5c065ff",
  "version": 1
}
rename: version 2
replace:
{
  "id": "94470b00-137d-4edd-a2f7-806069e2912a",
  "version": 1
}
invalid command: true
duplicate: uq_team_code
bad request: true
stale version: true
```

The ids of the created and replaced teams and the timestamps differ on every run; the seeded
ids do not. The last four lines are the errors a caller matches on. The malformed code is the
package's own `teams.ErrValidation`, rejected before any SQL runs. The duplicate is a
`sqlate.ConstraintError` whose `Constraint` is the name the schema gave it, `uq_team_code`, so
a program can map it to the field it reports. The bad filter value unwraps to
`query.ErrDirectives`, one check for any bad request. The stale version is
`query.ErrVersionMismatch`, the guard's own conflict.

## 12. Clean up

```sh
mise run db-down
```

The program's last step reverted the migration, and `db-down` drops the volume. The finished
tree:

```
teams/
├── compose.yml
├── database.go
├── go.mod
├── main.go
├── migrations/
│   ├── 0001_team.down.sql
│   └── 0001_team.up.sql
├── mise.toml
├── patterns/
│   └── identity.sql
├── run.go
├── schema.go
├── seed.go
├── seeds/
│   └── teams.json
├── sqlint.toml
├── stores.go
└── teams/
    ├── database.go
    ├── database_test.go
    ├── entities.go
    ├── patterns_test.go
    └── statements/
        ├── create.sql
        ├── delete.sql
        ├── edit.sql
        ├── find_by_codes.sql
        ├── seed.sql
        ├── team_view.sql
        └── version.sql
```

From here, the [features](features.md) document covers each mechanism in the detail the program
skipped, and the [glossary](glossary.md) defines the terms.
