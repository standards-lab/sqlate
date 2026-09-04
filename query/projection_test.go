package query_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

const base = "SELECT id, name, age FROM person"

type person struct {
	ID   string
	Name string
	Age  int64
}

func scanPerson(rows *sql.Rows) (person, error) {
	var p person
	err := rows.Scan(&p.ID, &p.Name, &p.Age)
	return p, err
}

// The base file ends with a semicolon, which the loader strips so the
// statement composes as a derived table.
var projectionFiles = fstest.MapFS{
	"sql/person_view.sql": {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid\n--| field: name text\n--| field: age integer\n" + base + ";\n")},
	"sql/no_contract.sql": {Data: []byte("--| tier: standard\nSELECT 1")},
	"sql/with_param.sql":  {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid\nSELECT id FROM t WHERE tenant = {{tenant}}")},
}

func projection(t *testing.T) query.Projection[person] {
	t.Helper()
	return catalog().MustCompile(projectionFiles, "sql", sqltest.Dialect{}).Statement("person_view").Project(scanPerson)
}

func count(n int64) sqltest.Response {
	return sqltest.Response{Columns: []string{"count"}, Rows: [][]driver.Value{{n}}}
}

func people(n int) sqltest.Response {
	r := sqltest.Response{Columns: []string{"id", "name", "age"}}
	for i := range n {
		r.Rows = append(r.Rows, []driver.Value{string(rune('a' + i)), "P", int64(20 + i)})
	}
	return r
}

func TestList_ComposesCountAndPageOverTheBase(t *testing.T) {
	db, rec := session(t, count(12), people(2))
	items, total, err := projection(t).List(context.Background(), db, query.Directives{
		Page:    query.Page{Number: 2, Size: 10},
		Sort:    []query.Sort{{Field: "name", Descending: true}},
		Filters: []query.Filter{{Field: "age", Op: query.OpGe, Value: "21"}},
	})
	if err != nil || total != 12 || len(items) != 2 || items[1].ID != "b" {
		t.Fatalf("List = %v, %d, %v", items, total, err)
	}
	calls := rec.Calls()
	if calls[0].SQL != "SELECT COUNT(*) FROM ("+base+") q WHERE q.age >= CAST($1 AS integer)" || calls[0].Args[0] != "21" {
		t.Errorf("count = %+v", calls[0])
	}
	if calls[1].SQL != "SELECT * FROM ("+base+") q WHERE q.age >= CAST($1 AS integer) ORDER BY q.name DESC, q.id OFFSET $2 ROWS FETCH NEXT $3 ROWS ONLY" {
		t.Errorf("page sql = %q", calls[1].SQL)
	}
	if a := calls[1].Args; a[0] != "21" || a[1] != 10 || a[2] != 10 {
		t.Errorf("page args = %v, want the filter value then offset 10, fetch 10", a)
	}
	if rec.RowsLeaked() != 0 || rec.Pending() != 0 {
		t.Errorf("leaked = %d, pending = %d", rec.RowsLeaked(), rec.Pending())
	}
}

func TestList_OperatorLowering(t *testing.T) {
	cases := map[query.Op]struct {
		value any
		want  string
		args  []any
	}{
		query.OpEq:        {"x", "q.name = CAST($1 AS text)", []any{"x"}},
		query.OpNe:        {"x", "q.name <> CAST($1 AS text)", []any{"x"}},
		query.OpGt:        {"x", "q.name > CAST($1 AS text)", []any{"x"}},
		query.OpGe:        {"x", "q.name >= CAST($1 AS text)", []any{"x"}},
		query.OpLt:        {"x", "q.name < CAST($1 AS text)", []any{"x"}},
		query.OpLe:        {"x", "q.name <= CAST($1 AS text)", []any{"x"}},
		query.OpLike:      {"x%", "q.name LIKE CAST($1 AS text)", []any{"x%"}},
		query.OpIsNull:    {nil, "q.name IS NULL", nil},
		query.OpIsNotNull: {nil, "q.name IS NOT NULL", nil},
		query.OpIn:        {[]any{"x", "y"}, "q.name IN (CAST($1 AS text), CAST($2 AS text))", []any{"x", "y"}},
	}
	for op, c := range cases {
		t.Run(string(op), func(t *testing.T) {
			db, rec := session(t, count(0), people(0))
			_, _, err := projection(t).List(context.Background(), db, query.Directives{
				Page:    query.Page{Number: 1, Size: 5},
				Filters: []query.Filter{{Field: "name", Op: op, Value: c.value}},
			})
			if err != nil {
				t.Fatal(err)
			}
			cnt := rec.Calls()[0]
			if cnt.SQL != "SELECT COUNT(*) FROM ("+base+") q WHERE "+c.want {
				t.Errorf("sql = %q", cnt.SQL)
			}
			if len(cnt.Args) != len(c.args) {
				t.Errorf("args = %v, want %v", cnt.Args, c.args)
			}
		})
	}
}

func TestList_KeyIsTheTieBreakerUnlessSortedBy(t *testing.T) {
	db, rec := session(t, count(0), people(0), count(0), people(0), count(0), people(0))
	p := projection(t)
	ctx := context.Background()
	_, _, _ = p.List(ctx, db, query.Directives{Page: query.Page{Number: 1, Size: 5}})
	_, _, _ = p.List(ctx, db, query.Directives{Page: query.Page{Number: 1, Size: 5}, Sort: []query.Sort{{Field: "id", Descending: true}}})
	_, _, _ = p.List(ctx, db, query.Directives{Page: query.Page{Number: 3, Size: 5}, Sort: []query.Sort{{Field: "age"}, {Field: "name", Descending: true}}})
	pages := rec.SQL(sqltest.OpQuery)
	for i, want := range []string{
		" ORDER BY q.id OFFSET $1 ROWS FETCH NEXT $2 ROWS ONLY",
		" ORDER BY q.id DESC OFFSET $1 ROWS FETCH NEXT $2 ROWS ONLY",
		" ORDER BY q.age, q.name DESC, q.id OFFSET $1 ROWS FETCH NEXT $2 ROWS ONLY",
	} {
		if got := pages[2*i+1]; !strings.HasSuffix(got, want) {
			t.Errorf("page %d = %q, want suffix %q", i, got, want)
		}
	}
	if a := rec.Calls()[5].Args; a[0] != 10 || a[1] != 5 {
		t.Errorf("page 3 of 5 bound %v, want offset 10, fetch 5", a)
	}
}

func TestList_DirectiveErrorsUnwrapToErrDirectivesBeforeAnyIO(t *testing.T) {
	db, rec := session(t)
	p := projection(t)
	ok := query.Page{Number: 1, Size: 5}
	cases := map[string]query.Directives{
		"page 0":         {Page: query.Page{Number: 0, Size: 5}},
		"size 0":         {Page: query.Page{Number: 1, Size: 0}},
		"unknown filter": {Page: ok, Filters: []query.Filter{{Field: "email", Op: query.OpEq, Value: "x"}}},
		"unknown sort":   {Page: ok, Sort: []query.Sort{{Field: "email"}}},
		"unknown op":     {Page: ok, Filters: []query.Filter{{Field: "name", Op: "matches", Value: "x"}}},
		"in not a slice": {Page: ok, Filters: []query.Filter{{Field: "name", Op: query.OpIn, Value: "x"}}},
		"in empty":       {Page: ok, Filters: []query.Filter{{Field: "name", Op: query.OpIn, Value: []any{}}}},
	}
	for name, d := range cases {
		_, _, err := p.List(context.Background(), db, d)
		if !errors.Is(err, query.ErrDirectives) {
			t.Errorf("%s: err = %v, want ErrDirectives", name, err)
		}
	}
	var unknown *query.UnknownFieldError
	_, _, err := p.List(context.Background(), db, cases["unknown sort"])
	if !errors.As(err, &unknown) || unknown.Use != query.FieldUseSort {
		t.Errorf("unknown sort = %v", err)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("rejected declarations reached the driver: %v", rec.Ops())
	}
}

// dataException is a driver error carrying an SQLSTATE, the shape an
// engine's dialect classifies.
type dataException struct{ code, msg string }

func (e dataException) Error() string    { return e.msg }
func (e dataException) SQLState() string { return e.code }

// invalidValueDialect maps SQLSTATE class 22 to ErrInvalidValue the way an
// engine sub-module does, over the stub dialect for everything else.
type invalidValueDialect struct{ sqltest.Dialect }

func (d invalidValueDialect) MapError(err error) error {
	if e, ok := errors.AsType[dataException](err); ok && strings.HasPrefix(e.SQLState(), "22") {
		return fmt.Errorf("%w: %w", sqlate.ErrInvalidValue, err)
	}
	return d.Dialect.MapError(err)
}

func TestList_EngineDataExceptionIsAnInvalidValue(t *testing.T) {
	pool, _ := sqltest.Open(t, sqltest.Response{Err: dataException{"22P02", `invalid input syntax for type uuid: "nope"`}})
	db := sqlate.Wrap(pool, invalidValueDialect{})
	_, _, err := projection(t).List(context.Background(), db, query.Directives{
		Page:    query.Page{Number: 1, Size: 5},
		Filters: []query.Filter{{Field: "id", Op: query.OpEq, Value: "nope"}},
	})
	var invalid *query.InvalidValueError
	if !errors.As(err, &invalid) || !errors.Is(err, query.ErrDirectives) || !errors.Is(err, sqlate.ErrInvalidValue) {
		t.Fatalf("err = %v, want an InvalidValueError from the engine", err)
	}
	if !strings.Contains(err.Error(), "invalid input syntax for type uuid") {
		t.Errorf("the engine's reason is lost: %v", err)
	}
	if _, err := projection(t).One(context.Background(), db, "id", "nope"); err == nil {
		t.Error("One did not fail")
	}
}

func TestOne_ByField(t *testing.T) {
	db, rec := session(t, people(1), people(0))
	p := projection(t)
	got, err := p.One(context.Background(), db, "name", "P")
	if err != nil || got.ID != "a" {
		t.Fatalf("One = %+v, %v", got, err)
	}
	if rec.Calls()[0].SQL != "SELECT * FROM ("+base+") q WHERE q.name = CAST($1 AS text)" {
		t.Errorf("sql = %q", rec.Calls()[0].SQL)
	}
	if _, err := p.One(context.Background(), db, "name", "Q"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("no row = %v, want sql.ErrNoRows", err)
	}
	if _, err := p.One(context.Background(), db, "email", "x"); !errors.Is(err, query.ErrDirectives) {
		t.Errorf("unknown field = %v", err)
	}
}

func TestVerify_ProbesEveryContractField(t *testing.T) {
	db, rec := session(t)
	if err := projection(t).Verify(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if got := rec.SQL(sqltest.OpPrepare); len(got) != 1 || got[0] != "SELECT q.id, q.name, q.age FROM ("+base+") q" {
		t.Errorf("probe = %v", got)
	}
	rec.FailPrepare = func(string) error { return errors.New(`column q.age does not exist`) }
	if err := projection(t).Verify(context.Background(), db); err == nil || !strings.Contains(err.Error(), "person_view: field contract") {
		t.Errorf("Verify = %v, want the base named", err)
	}
}

func TestProject_RequiresAContractAndNoBaseParameters(t *testing.T) {
	stmts := catalog().MustCompile(projectionFiles, "sql", sqltest.Dialect{})
	for _, name := range []string{"no_contract", "with_param"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Project(%s) did not panic", name)
				}
			}()
			stmts.Statement(name).Project(scanPerson)
		}()
	}
}
