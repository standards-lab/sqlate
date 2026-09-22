package postgres_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	"github.com/standards-lab/sqlate/postgres"
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
