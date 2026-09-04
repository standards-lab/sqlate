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
	"github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/sqltest"
)

func TestDialect_NameAndPlaceholder(t *testing.T) {
	d := postgres.Dialect{}
	if d.Name() != "postgres" || d.Placeholder(3) != "$3" {
		t.Errorf("dialect = %s %s", d.Name(), d.Placeholder(3))
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
		pgErr := &pgconn.PgError{Code: c.code, ConstraintName: "uq_organization_parent_code"}
		err := d.MapError(fmt.Errorf("exec: %w", pgErr))
		if !errors.Is(err, c.class) {
			t.Errorf("%s: errors.Is(err, class) = false; err = %v", name, err)
		}
		ce, ok := errors.AsType[*sqlate.ConstraintError](err)
		if !ok || ce.Constraint != "uq_organization_parent_code" {
			t.Errorf("%s: error = %v, want ConstraintError carrying the constraint name", name, err)
			continue
		}
		if reached, ok := errors.AsType[*pgconn.PgError](err); !ok || reached != pgErr {
			t.Errorf("%s: the driver error is no longer reachable through the wrap", name)
		}
	}
}

func TestMapError_DataExceptionIsErrInvalidValue(t *testing.T) {
	d := postgres.Dialect{}
	pgErr := &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type uuid: "nope"`}
	err := d.MapError(pgErr)
	if !errors.Is(err, sqlate.ErrInvalidValue) || !strings.Contains(err.Error(), "invalid input syntax") {
		t.Errorf("class 22 = %v, want ErrInvalidValue carrying the engine error", err)
	}
	if reached, ok := errors.AsType[*pgconn.PgError](err); !ok || reached != pgErr {
		t.Error("the driver error is no longer reachable through the wrap")
	}
}

func TestMapError_PassesUnclassifiedThrough(t *testing.T) {
	d := postgres.Dialect{}
	serialization := &pgconn.PgError{Code: "40001"}
	if got := d.MapError(serialization); got != error(serialization) {
		t.Errorf("MapError(40001) = %v, want the error unchanged", got)
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
