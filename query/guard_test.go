package query_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

var guardFiles = fstest.MapFS{
	"sql/edit.sql":    {Data: []byte("--| tier: standard\nUPDATE organization SET name = {{name}}, version = version + 1 WHERE id = {{id}} AND version = {{version}}")},
	"sql/version.sql": {Data: []byte("--| tier: standard\nSELECT version FROM organization WHERE id = {{id}}")},
}

func guard(t *testing.T) query.Guard {
	t.Helper()
	stmts := catalog().MustCompile(guardFiles, "sql", sqltest.Dialect{})
	return stmts.Statement("edit").Guarded(stmts.Statement("version"), "version")
}

func versionRow(v int64) sqltest.Response {
	return sqltest.Response{Columns: []string{"version"}, Rows: [][]driver.Value{{v}}}
}

func TestGuard_RowAffectedIsTheNewVersionWithNoSecondRoundTrip(t *testing.T) {
	db, rec := session(t, sqltest.Response{Affected: 1})
	args := query.Args{"id": "x", "name": "New"}
	v, err := guard(t).Run(context.Background(), db, 3, args)
	if err != nil || v != 4 {
		t.Fatalf("Run = %d, %v", v, err)
	}
	calls := rec.Calls()
	if len(calls) != 1 || calls[0].Args[0] != "New" || calls[0].Args[1] != "x" || calls[0].Args[2] != int64(3) {
		t.Errorf("calls = %+v", calls)
	}
	if _, bound := args["version"]; bound {
		t.Error("Run wrote into the caller's Args")
	}
}

func TestGuard_MissClassifiesNotFoundAndMismatch(t *testing.T) {
	db, rec := session(t,
		sqltest.Response{Affected: 0}, sqltest.Response{Columns: []string{"version"}},
		sqltest.Response{Affected: 0}, versionRow(5),
	)
	g := guard(t)
	if _, err := g.Run(context.Background(), db, 3, query.Args{"id": "gone", "name": "n"}); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("gone = %v, want sql.ErrNoRows", err)
	}
	_, err := g.Run(context.Background(), db, 3, query.Args{"id": "x", "name": "n"})
	if !errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "expected 3, current 5") {
		t.Errorf("moved = %v, want ErrVersionMismatch with both versions", err)
	}
	if check := rec.Calls()[3]; check.SQL != "SELECT version FROM organization WHERE id = $1" || check.Args[0] != "x" {
		t.Errorf("check = %+v", check)
	}
}

func TestGuard_CommandFailurePassesThroughMapped(t *testing.T) {
	db, _ := session(t, sqltest.Response{Err: errDriver})
	_, err := guard(t).Run(context.Background(), db, 1, query.Args{"id": "x", "name": "n"})
	if _, ok := errors.AsType[*sqltest.MappedError](err); !ok {
		t.Errorf("err = %v, want mapped", err)
	}
}
