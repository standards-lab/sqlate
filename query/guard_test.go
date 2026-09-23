package query_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

var guardFiles = fstest.MapFS{
	"sql/edit.sql":    {Data: []byte("--| tier: standard\nUPDATE organization SET name = {{name}}, version = version + 1 WHERE id = {{id}} AND version = {{version}}")},
	"sql/version.sql": {Data: []byte("--| tier: standard\nSELECT version FROM organization WHERE id = {{id}}")},
	// The claim command's WHERE carries a predicate of its own, so a row at
	// the expected version can still refuse the write; it reads its row back
	// through item.
	"sql/claim.sql": {Data: []byte("--| tier: standard\n--| returning: item\nUPDATE item SET owner = {{owner}}, version = version + 1 WHERE id = {{id}} AND version = {{version}} AND status = 'pending'")},
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

// rowGuard is the claim guard compiled for d: sqltest.Dialect runs the
// fallback, sqltest.ReturningDialect the single-statement form.
func rowGuard(t *testing.T, d sqlate.Dialect) query.RowGuard[item] {
	t.Helper()
	stmts := catalog().MustCompile(guardFiles, "sql", d)
	return stmts.Statement("claim").Returning(scanItem).Guarded("version", func(i item) int64 { return i.Version })
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

// forms are the two ways a returning command runs: the fallback, the
// command and then its read in one transaction, and the single-statement
// form of a dialect with query.Returner.
var forms = []struct {
	name    string
	dialect sqlate.Dialect
	native  bool
}{
	{"fallback", sqltest.Dialect{}, false},
	{"native", sqltest.ReturningDialect{}, true},
}

// statements counts the exec and query calls, the statements the engine ran.
func statements(rec *sqltest.Recorder) int {
	n := 0
	for _, op := range rec.Ops() {
		if op == sqltest.OpExec || op == sqltest.OpQuery {
			n++
		}
	}
	return n
}

// firstStatement is the first exec or query call, the command.
func firstStatement(rec *sqltest.Recorder) sqltest.Call {
	calls := rec.Calls()
	i := slices.IndexFunc(calls, func(c sqltest.Call) bool { return c.Op == sqltest.OpExec || c.Op == sqltest.OpQuery })
	if i < 0 {
		return sqltest.Call{}
	}
	return calls[i]
}

func TestRowGuard_Outcomes(t *testing.T) {
	empty := sqltest.Response{Columns: []string{"id", "status", "version"}}
	cases := []struct {
		name             string
		fallback, native []sqltest.Response
		statements       [2]int // fallback, native
		check            func(t *testing.T, row item, err error)
	}{
		{
			name:       "changed",
			fallback:   []sqltest.Response{{Affected: 1}, itemRow("claimed", 4)},
			native:     []sqltest.Response{itemRow("claimed", 4)},
			statements: [2]int{2, 1},
			check: func(t *testing.T, row item, err error) {
				if err != nil || row.Version != 4 || row.Status != "claimed" {
					t.Errorf("Run = %+v, %v, want the changed row", row, err)
				}
			},
		},
		{
			name:       "no row",
			fallback:   []sqltest.Response{{Affected: 0}, empty},
			native:     []sqltest.Response{empty, empty},
			statements: [2]int{2, 2},
			check: func(t *testing.T, _ item, err error) {
				if err != sql.ErrNoRows {
					t.Errorf("err = %v, want bare sql.ErrNoRows", err)
				}
			},
		},
		{
			name:       "mismatch",
			fallback:   []sqltest.Response{{Affected: 0}, itemRow("pending", 5)},
			native:     []sqltest.Response{empty, itemRow("pending", 5)},
			statements: [2]int{2, 2},
			check: func(t *testing.T, _ item, err error) {
				if !errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "expected 3, current 5") {
					t.Errorf("err = %v, want ErrVersionMismatch with both versions", err)
				}
			},
		},
		{
			name:       "refused",
			fallback:   []sqltest.Response{{Affected: 0}, itemRow("done", 3)},
			native:     []sqltest.Response{empty, itemRow("done", 3)},
			statements: [2]int{2, 2},
			check: func(t *testing.T, _ item, err error) {
				refused, ok := errors.AsType[*query.RefusedError[item]](err)
				if !ok || refused.Version != 3 || refused.Row.ID != "x" || refused.Row.Status != "done" {
					t.Fatalf("err = %v, want *query.RefusedError[item] carrying the row", err)
				}
				if !errors.Is(err, query.ErrRefused) {
					t.Errorf("err = %v, want ErrRefused", err)
				}
			},
		},
	}
	for _, f := range forms {
		for _, c := range cases {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				script, want := c.fallback, c.statements[0]
				if f.native {
					script, want = c.native, c.statements[1]
				}
				pool, rec := sqltest.Open(t, script...)
				db := sqlate.Wrap(pool, f.dialect)
				args := query.Args{"id": "x", "owner": "me"}
				row, err := rowGuard(t, f.dialect).Run(context.Background(), db, 3, args)
				c.check(t, row, err)
				if got := statements(rec); got != want || rec.Pending() != 0 {
					t.Errorf("ran %d statements (%v), %d responses left; want %d", got, rec.Ops(), rec.Pending(), want)
				}
				if first := firstStatement(rec); first.Args[0] != "me" || first.Args[1] != "x" || first.Args[2] != int64(3) {
					t.Errorf("command args = %v, want owner, id, the bound version", first.Args)
				}
				if _, bound := args["version"]; bound {
					t.Error("Run wrote into the caller's Args")
				}
			})
		}
	}
}

func TestRowGuard_CommandFailurePassesThroughMapped(t *testing.T) {
	for _, f := range forms {
		pool, _ := sqltest.Open(t, sqltest.Response{Err: errDriver})
		db := sqlate.Wrap(pool, f.dialect)
		_, err := rowGuard(t, f.dialect).Run(context.Background(), db, 1, query.Args{"id": "x", "owner": "me"})
		if _, ok := errors.AsType[*sqltest.MappedError](err); !ok || !errors.Is(err, errDriver) {
			t.Errorf("%s: err = %v, want the driver's error mapped", f.name, err)
		}
	}
}

func TestRowGuard_PanicsOnAVersionTheCommandDoesNotTake(t *testing.T) {
	stmts := catalog().MustCompile(guardFiles, "sql", sqltest.Dialect{})
	defer func() {
		r, _ := recover().(string)
		if !strings.Contains(r, "claim") || !strings.Contains(r, `"rev"`) {
			t.Errorf("recover() = %q, want a panic naming the command and the parameter", r)
		}
	}()
	stmts.Statement("claim").Returning(scanItem).Guarded("rev", func(i item) int64 { return i.Version })
}
