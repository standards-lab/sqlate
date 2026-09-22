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
	// The claim command's WHERE carries a predicate of its own, so a row at
	// the expected version can still refuse the write.
	"sql/claim.sql": {Data: []byte("--| tier: standard\nUPDATE item SET owner = {{owner}}, version = version + 1 WHERE id = {{id}} AND version = {{version}} AND status = 'pending'")},
	"sql/item.sql":  {Data: []byte("--| tier: standard\nSELECT id, status, version FROM item WHERE id = {{id}}")},
}

type item struct {
	ID      string
	Status  string
	Version int64
}

func scanItem(rows *sql.Rows) (item, error) {
	var i item
	err := rows.Scan(&i.ID, &i.Status, &i.Version)
	return i, err
}

func guard(t *testing.T) query.Guard {
	t.Helper()
	stmts := catalog().MustCompile(guardFiles, "sql", sqltest.Dialect{})
	return stmts.Statement("edit").Guarded(stmts.Statement("version"), "version")
}

func rowGuard(t *testing.T) query.RowGuard[item] {
	t.Helper()
	stmts := catalog().MustCompile(guardFiles, "sql", sqltest.Dialect{})
	check := stmts.Statement("item").Scan(scanItem)
	return stmts.Statement("claim").GuardedRow(check, "version", func(i item) int64 { return i.Version })
}

func versionRow(v int64) sqltest.Response {
	return sqltest.Response{Columns: []string{"version"}, Rows: [][]driver.Value{{v}}}
}

func itemRow(status string, v int64) sqltest.Response {
	return sqltest.Response{
		Columns: []string{"id", "status", "version"},
		Rows:    [][]driver.Value{{"x", status, v}},
	}
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

func TestRowGuard_RowAffectedIsTheNewVersionWithNoSecondRoundTrip(t *testing.T) {
	db, rec := session(t, sqltest.Response{Affected: 1})
	args := query.Args{"id": "x", "owner": "me"}
	v, err := rowGuard(t).Run(context.Background(), db, 3, args)
	if err != nil || v != 4 {
		t.Fatalf("Run = %d, %v", v, err)
	}
	calls := rec.Calls()
	if len(calls) != 1 || calls[0].Args[0] != "me" || calls[0].Args[1] != "x" || calls[0].Args[2] != int64(3) {
		t.Errorf("calls = %+v", calls)
	}
	if _, bound := args["version"]; bound {
		t.Error("Run wrote into the caller's Args")
	}
}

func TestRowGuard_MissAndMismatchClassifyLikeThePlainGuard(t *testing.T) {
	db, rec := session(t,
		sqltest.Response{Affected: 0}, sqltest.Response{Columns: []string{"id", "status", "version"}},
		sqltest.Response{Affected: 0}, itemRow("pending", 5),
	)
	g := rowGuard(t)
	if _, err := g.Run(context.Background(), db, 3, query.Args{"id": "gone", "owner": "me"}); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("gone = %v, want sql.ErrNoRows", err)
	}
	_, err := g.Run(context.Background(), db, 3, query.Args{"id": "x", "owner": "me"})
	if !errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "expected 3, current 5") {
		t.Errorf("moved = %v, want ErrVersionMismatch with both versions", err)
	}
	if check := rec.Calls()[3]; check.SQL != "SELECT id, status, version FROM item WHERE id = $1" || check.Args[0] != "x" {
		t.Errorf("check = %+v", check)
	}
}

func TestRowGuard_RefusedCarriesTheRow(t *testing.T) {
	db, _ := session(t, sqltest.Response{Affected: 0}, itemRow("done", 3))
	_, err := rowGuard(t).Run(context.Background(), db, 3, query.Args{"id": "x", "owner": "me"})
	var refused *query.RefusedError[item]
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want *query.RefusedError[item]", err)
	}
	if refused.Version != 3 || refused.Row.ID != "x" || refused.Row.Status != "done" {
		t.Errorf("refused = %+v", refused)
	}
	if !errors.Is(err, query.ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
}
