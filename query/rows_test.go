package query_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

var errDriver = errors.New("engine says no")

type org struct {
	ID   string
	Name string
}

func scanOrg(rows *sql.Rows) (org, error) {
	var o org
	err := rows.Scan(&o.ID, &o.Name)
	return o, err
}

func orgRows(n int) sqltest.Response {
	r := sqltest.Response{Columns: []string{"id", "name"}}
	for i := range n {
		r.Rows = append(r.Rows, []driver.Value{string(rune('a' + i)), "Org"})
	}
	return r
}

func session(t *testing.T, responses ...sqltest.Response) (*sqlate.DB, *sqltest.Recorder) {
	t.Helper()
	pool, rec := sqltest.Open(t, responses...)
	return sqlate.Wrap(pool, sqltest.Dialect{}), rec
}

var testFiles = fstest.MapFS{
	"sql/by_id.sql":   {Data: []byte("--| tier: standard\nSELECT id, name FROM organization WHERE id = {{id}}")},
	"sql/all.sql":     {Data: []byte("--| tier: standard\nSELECT id, name FROM organization")},
	"sql/count.sql":   {Data: []byte("--| tier: standard\nSELECT COUNT(*) FROM organization WHERE parent_id = {{parent}}")},
	"sql/edit.sql":    {Data: []byte("--| tier: standard\nUPDATE organization SET name = {{name}} WHERE id = {{id}} AND version = {{version}}")},
	"sql/lock.sql":    {Data: []byte("--| tier: native\n--| native: postgres\n--| transaction: required\nSELECT pg_advisory_xact_lock({{key}})")},
	"sql/in_tree.sql": {Data: []byte("--| tier: standard\nSELECT COUNT(*) FROM t WHERE node = {{node}} AND (a = {{candidate}} OR b = {{node}})")},
}

// catalog is the library's own patterns, the catalog every test compiles
// against unless it registers more.
func catalog() *query.Catalog { return query.MustCatalog(query.Patterns()) }

func source(t *testing.T) *query.Statements {
	t.Helper()
	return catalog().MustCompile(testFiles, "sql", sqltest.Dialect{})
}

func TestExec_BindsByNameInPositionOrder(t *testing.T) {
	db, rec := session(t, sqltest.Response{Affected: 1})
	edit := source(t).Statement("edit")
	n, err := edit.Exec(context.Background(), db, query.Args{"id": "x", "version": int64(3), "name": "New", "extra": "ignored"})
	if err != nil || n != 1 {
		t.Fatalf("Exec = %d, %v", n, err)
	}
	call := rec.Calls()[0]
	if call.SQL != "UPDATE organization SET name = $1 WHERE id = $2 AND version = $3" {
		t.Errorf("sql = %q", call.SQL)
	}
	if call.Args[0] != "New" || call.Args[1] != "x" || call.Args[2] != int64(3) {
		t.Errorf("args = %v", call.Args)
	}

	_, err = edit.Exec(context.Background(), db, query.Args{"id": "x", "version": 1})
	var missing *query.ArgumentError
	if !errors.As(err, &missing) || missing.Name != "name" || missing.Statement != "edit" {
		t.Errorf("missing argument: err = %v", err)
	}
	if len(rec.Calls()) != 1 {
		t.Error("a missing argument reached the driver")
	}
}

func TestExec_RepeatedNameBindsOnce(t *testing.T) {
	db, rec := session(t, sqltest.Response{Columns: []string{"count"}, Rows: [][]driver.Value{{int64(1)}}})
	n, err := source(t).Statement("in_tree").Scan(query.Scalar[int64]).One(context.Background(), db, query.Args{"node": "n", "candidate": "c"})
	if err != nil || n != 1 {
		t.Fatalf("One = %d, %v", n, err)
	}
	if args := rec.Calls()[0].Args; len(args) != 2 || args[0] != "n" || args[1] != "c" {
		t.Errorf("args = %v, want node then candidate, once each", args)
	}
}

func TestRows_OneAllEach(t *testing.T) {
	ctx := context.Background()
	stmts := source(t)
	all := stmts.Statement("all").Scan(scanOrg)
	byID := stmts.Statement("by_id").Scan(scanOrg)

	db, rec := session(t, orgRows(1), orgRows(0), orgRows(3), orgRows(3))
	o, err := byID.One(ctx, db, query.Args{"id": "a"})
	if err != nil || o.ID != "a" {
		t.Errorf("One = %+v, %v", o, err)
	}
	if _, err := byID.One(ctx, db, query.Args{"id": "zz"}); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("One on no rows = %v, want sql.ErrNoRows unmapped", err)
	}
	os, err := all.All(ctx, db, nil)
	if err != nil || len(os) != 3 || os[2].ID != "c" {
		t.Errorf("All = %v, %v", os, err)
	}
	seen := 0
	for o, err := range all.Each(ctx, db, nil) {
		if err != nil {
			t.Fatal(err)
		}
		seen++
		if o.ID == "b" {
			break
		}
	}
	if seen != 2 {
		t.Errorf("Each yielded %d before the break, want 2", seen)
	}
	if rec.RowsLeaked() != 0 {
		t.Errorf("%d row sets leaked; Each must close on break", rec.RowsLeaked())
	}
}

func TestRows_ErrorsCrossTheMappingBoundary(t *testing.T) {
	ctx := context.Background()
	all := source(t).Statement("all").Scan(scanOrg)
	var mapped *sqltest.MappedError

	db, _ := session(t, sqltest.Response{Err: errDriver})
	if _, err := all.All(ctx, db, nil); !errors.As(err, &mapped) || !errors.Is(err, errDriver) {
		t.Errorf("query error = %v, want mapped", err)
	}

	// A scan failure: the row is narrower than the scan function reads.
	db, rec := session(t, sqltest.Response{Columns: []string{"id"}, Rows: [][]driver.Value{{"a"}}})
	if _, err := all.One(ctx, db, nil); !errors.As(err, &mapped) {
		t.Errorf("scan error = %v, want mapped through ErrorMapper", err)
	}
	if rec.RowsLeaked() != 0 {
		t.Error("rows leaked after a scan failure")
	}
}

func TestExec_TransactionRequired(t *testing.T) {
	ctx := context.Background()
	db, rec := session(t, sqltest.Response{Affected: 0})
	lock := source(t).Statement("lock")
	if _, err := lock.Exec(ctx, db, query.Args{"key": int64(1)}); !errors.Is(err, query.ErrTransactionRequired) {
		t.Fatalf("Exec on the pool = %v, want ErrTransactionRequired", err)
	}
	if len(rec.Calls()) != 0 {
		t.Error("the statement reached the driver outside a transaction")
	}
	_, err := db.Transact(ctx, func(tx *sqlate.Tx) (struct{}, error) {
		_, err := lock.Exec(ctx, tx, query.Args{"key": int64(1)})
		return struct{}{}, err
	})
	if err != nil {
		t.Errorf("Exec in a transaction: %v", err)
	}
}
