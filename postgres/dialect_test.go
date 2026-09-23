package postgres_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	"github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

func TestDialect_NameAndPlaceholder(t *testing.T) {
	d := postgres.Dialect{}
	if d.Name() != "postgres" || d.Placeholder(3) != "$3" {
		t.Errorf("dialect = %s %s", d.Name(), d.Placeholder(3))
	}
}

func TestDialect_ServerVersion(t *testing.T) {
	if got := (postgres.Dialect{}).ServerVersion(); got != "SELECT version()" {
		t.Errorf("ServerVersion() = %q", got)
	}
}

func TestDialect_CreateHistoryDelegatesToStandardCatalog(t *testing.T) {
	want := migrate.StandardCatalog{}.CreateHistory("schema_version")
	if got := (postgres.Dialect{}).CreateHistory("schema_version"); got != want {
		t.Errorf("CreateHistory() = %q, want %q", got, want)
	}
}

func TestDialect_HistoryExistsQualifiesByCurrentSchema(t *testing.T) {
	want := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1"
	if got := (postgres.Dialect{}).HistoryExists("$1"); got != want {
		t.Errorf("HistoryExists() = %q, want %q", got, want)
	}
}

func TestDialect_ReturningAppendsTheClauseForInsertAndUpdate(t *testing.T) {
	cols := []string{"id", "status"}
	for _, tc := range []struct {
		verb query.Verb
		body string
	}{
		{query.Insert, "INSERT INTO item (id) VALUES ({{id}})"},
		{query.Update, "UPDATE item SET status = 'x' WHERE id = {{id}}"},
	} {
		got, ok := (postgres.Dialect{}).Returning(tc.verb, tc.body, cols)
		if want := tc.body + "\nRETURNING id, status"; !ok || got != want {
			t.Errorf("%s: Returning = %q, %v; want %q, true", tc.verb, got, ok, want)
		}
	}
}

func TestDialect_ReturningDeclinesAnUnknownVerb(t *testing.T) {
	if got, ok := (postgres.Dialect{}).Returning(query.Verb("DELETE"), "DELETE FROM item", []string{"id"}); ok || got != "" {
		t.Errorf("Returning(DELETE) = %q, %v; want a decline", got, ok)
	}
}

// A body ending in a line comment keeps the clause: it starts on a line of
// its own, outside the comment.
func TestDialect_ReturningAfterATrailingLineComment(t *testing.T) {
	body := "UPDATE item SET status = 'x' WHERE id = {{id}} -- the key"
	got, ok := (postgres.Dialect{}).Returning(query.Update, body, []string{"id"})
	if !ok || !strings.HasSuffix(got, "-- the key\nRETURNING id") {
		t.Errorf("Returning = %q, %v; want the clause on its own line after the comment", got, ok)
	}
}

var returningFiles = fstest.MapFS{
	"sql/item_by_id.sql":  {Data: []byte("--| tier: standard\nSELECT i.a, i.b FROM item i WHERE i.id = {{id}}")},
	"sql/create_item.sql": {Data: []byte("--| tier: standard\n--| returning: item_by_id\nINSERT INTO item (id, a, b) VALUES ({{id}}, {{a}}, {{b}})")},
}

// Compiled through query, the command's single-statement form carries the
// read's bare columns and the engine's placeholders.
func TestDialect_ReturningCompilesThroughQuery(t *testing.T) {
	stmts := query.MustCatalog(postgres.Patterns()).MustCompile(returningFiles, "sql", postgres.Dialect{})
	st := stmts.Statement("create_item")
	want := "INSERT INTO item (id, a, b) VALUES ($1, $2, $3)\nRETURNING a, b"
	if got := st.ReturningText(); got != want {
		t.Errorf("ReturningText = %q, want %q", got, want)
	}
	if st.Reads() != "item_by_id" {
		t.Errorf("Reads = %q, want item_by_id", st.Reads())
	}
}

func TestMapError_ClassifiesConstraints(t *testing.T) {
	d := postgres.Dialect{}
	cases := map[string]struct {
		code  string
		class error
	}{
		"unique":      {"23505", sqlate.ErrUniqueViolation},
		"foreign key": {"23503", sqlate.ErrForeignKeyViolation},
		"check":       {"23514", sqlate.ErrCheckViolation},
		"not null":    {"23502", sqlate.ErrNotNullViolation},
	}
	for name, c := range cases {
		pgErr := &pgconn.PgError{
			Code:           c.code,
			ConstraintName: "uq_organization_parent_code",
			TableName:      "organization",
			ColumnName:     "parent_code",
		}
		err := d.MapError(fmt.Errorf("exec: %w", pgErr))
		if !errors.Is(err, c.class) {
			t.Errorf("%s: errors.Is(err, class) = false; err = %v", name, err)
		}
		ce, ok := errors.AsType[*sqlate.ConstraintError](err)
		if !ok || ce.Constraint != "uq_organization_parent_code" {
			t.Errorf("%s: error = %v, want a ConstraintError with the constraint name", name, err)
			continue
		}
		if ce.Table != "organization" || ce.Column != "parent_code" {
			t.Errorf("%s: table, column = %q, %q, want organization, parent_code", name, ce.Table, ce.Column)
		}
		if found, ok := errors.AsType[*pgconn.PgError](err); !ok || found != pgErr {
			t.Errorf("%s: errors.As no longer finds the driver error through the wrap", name)
		}
	}
}

func TestMapError_NotNullWithoutConstraintNameCarriesColumn(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23502", TableName: "organization", ColumnName: "name"}
	err := (postgres.Dialect{}).MapError(pgErr)
	if !errors.Is(err, sqlate.ErrNotNullViolation) {
		t.Errorf("23502 = %v, want ErrNotNullViolation", err)
	}
	ce, ok := errors.AsType[*sqlate.ConstraintError](err)
	if !ok {
		t.Fatalf("23502 = %v, want a ConstraintError", err)
	}
	if ce.Constraint != "" || ce.Table != "organization" || ce.Column != "name" {
		t.Errorf("constraint, table, column = %q, %q, %q, want \"\", organization, name", ce.Constraint, ce.Table, ce.Column)
	}
}

func TestMapError_DependentObjectsAndSerializationFailure(t *testing.T) {
	d := postgres.Dialect{}
	cases := map[string]struct {
		code  string
		class error
	}{
		"dependent objects":     {"2BP01", sqlate.ErrDependentObjects},
		"serialization failure": {"40001", sqlate.ErrSerializationFailure},
	}
	for name, c := range cases {
		pgErr := &pgconn.PgError{Code: c.code, Message: "engine message"}
		err := d.MapError(fmt.Errorf("exec: %w", pgErr))
		if !errors.Is(err, c.class) || !strings.Contains(err.Error(), "engine message") {
			t.Errorf("%s: %v, want the class sentinel wrapping the engine error", name, err)
		}
		if _, ok := errors.AsType[*sqlate.ConstraintError](err); ok {
			t.Errorf("%s: mapped to a ConstraintError, want the plain dual wrap", name)
		}
		if found, ok := errors.AsType[*pgconn.PgError](err); !ok || found != pgErr {
			t.Errorf("%s: errors.As no longer finds the driver error through the wrap", name)
		}
	}
}

func TestMapError_DataExceptionIsErrInvalidValue(t *testing.T) {
	d := postgres.Dialect{}
	pgErr := &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type uuid: "nope"`}
	err := d.MapError(pgErr)
	if !errors.Is(err, sqlate.ErrInvalidValue) || !strings.Contains(err.Error(), "invalid input syntax") {
		t.Errorf("class 22 = %v, want ErrInvalidValue wrapping the engine error", err)
	}
	if found, ok := errors.AsType[*pgconn.PgError](err); !ok || found != pgErr {
		t.Error("errors.As no longer finds the driver error through the wrap")
	}
}

func TestMapError_PassesUnclassifiedThrough(t *testing.T) {
	d := postgres.Dialect{}
	deadlock := &pgconn.PgError{Code: "40P01"}
	if got := d.MapError(deadlock); got != error(deadlock) {
		t.Errorf("MapError(40P01) = %v, want the error unchanged", got)
	}
	if got := d.MapError(sql.ErrNoRows); got != sql.ErrNoRows {
		t.Errorf("MapError(sql.ErrNoRows) = %v, want it untouched", got)
	}
	plain := errors.New("not a driver error")
	if got := d.MapError(plain); got != plain {
		t.Errorf("MapError(plain) = %v, want it untouched", got)
	}
	if d.MapError(nil) != nil {
		t.Error("nil maps to nil")
	}
}

func TestLockUnlock_IssueTheAdvisoryCallsOnTheConnection(t *testing.T) {
	ctx := context.Background()
	pool, rec := sqltest.Open(t,
		sqltest.Response{Affected: 0},
		sqltest.Response{Columns: []string{"pg_advisory_unlock"}, Rows: [][]driver.Value{{true}}},
	)
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer func() { _ = conn.Close() }()

	d := postgres.Dialect{}
	if err := d.Lock(ctx, conn, "migrate.schema_version"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := d.Unlock(ctx, conn, "migrate.schema_version"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	calls := rec.Calls()
	if calls[0].SQL != "SELECT pg_advisory_lock(hashtext($1))" || calls[0].Args[0] != "migrate.schema_version" {
		t.Errorf("lock call = %+v", calls[0])
	}
	if calls[1].SQL != "SELECT pg_advisory_unlock(hashtext($1))" || calls[1].Args[0] != "migrate.schema_version" {
		t.Errorf("unlock call = %+v", calls[1])
	}
}

func TestUnlock_FalseIsErrLockNotHeld(t *testing.T) {
	ctx := context.Background()
	pool, _ := sqltest.Open(t,
		sqltest.Response{Columns: []string{"pg_advisory_unlock"}, Rows: [][]driver.Value{{false}}},
	)
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := (postgres.Dialect{}).Unlock(ctx, conn, "x"); !errors.Is(err, postgres.ErrLockNotHeld) {
		t.Errorf("Unlock = %v, want ErrLockNotHeld", err)
	}
}
