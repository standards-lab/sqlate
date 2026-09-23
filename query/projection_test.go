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

func scanPerson(rows query.Row) (person, error) {
	var p person
	err := rows.Scan(&p.ID, &p.Name, &p.Age)
	return p, err
}

// The base file ends with a semicolon, which the loader strips so the
// statement composes as a derived table.
var projectionFiles = fstest.MapFS{
	"sql/person_view.sql":    {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid\n--| field: name text\n--| field: age integer\n" + base + ";\n")},
	"sql/no_contract.sql":    {Data: []byte("--| tier: standard\nSELECT 1")},
	"sql/with_param.sql":     {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid\nSELECT id FROM t WHERE tenant = {{tenant}}")},
	"sql/two_params.sql":     {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid\n--| field: name text\nSELECT id, name FROM t WHERE tenant = {{tenant}} AND region = {{region}}")},
	"sql/expanded_param.sql": {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid\nSELECT id FROM t WHERE id IN ({{ids...}})")},
	// A composite key whose fields are declared not null: the contract a
	// cursor needs.
	"sql/member.sql": {Data: []byte("--| tier: standard\n--| key: org, id\n--| field: org uuid not null\n--| field: id uuid not null\n--| field: name text\nSELECT org, id, name FROM member")},
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
	got, err := projection(t).List(context.Background(), db, query.Directives{
		Sort:    []query.Sort{{Field: "name", Descending: true}},
		Filters: []query.Filter{{Field: "age", Op: query.OpGe, Value: "21"}},
	}, query.Page{Number: 2, Size: 10})
	if err != nil || got.Total != 12 || len(got.Items) != 2 || got.Items[1].ID != "b" {
		t.Fatalf("List = %+v, %v", got, err)
	}
	if got.More || got.Next != "" {
		t.Errorf("a short page reported more = %v, next = %q", got.More, got.Next)
	}
	calls := rec.Calls()
	if calls[0].SQL != "SELECT COUNT(*) FROM ("+base+") q WHERE q.age >= CAST($1 AS integer)" || calls[0].Args[0] != "21" {
		t.Errorf("count = %+v", calls[0])
	}
	if calls[1].SQL != "SELECT * FROM ("+base+") q WHERE q.age >= CAST($1 AS integer) ORDER BY q.name DESC, q.id DESC OFFSET $2 ROWS FETCH NEXT $3 ROWS ONLY" {
		t.Errorf("page sql = %q", calls[1].SQL)
	}
	if a := calls[1].Args; a[0] != "21" || a[1] != 10 || a[2] != 11 {
		t.Errorf("page args = %v, want the filter value then offset 10, fetch 11", a)
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
			_, err := projection(t).List(context.Background(), db, query.Directives{
				Filters: []query.Filter{{Field: "name", Op: op, Value: c.value}},
			}, query.Page{Number: 1, Size: 5})
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
	db, rec := session(t, count(0), people(0), count(0), people(0), count(0), people(0), count(0), people(0))
	p := projection(t)
	ctx := context.Background()
	_, _ = p.List(ctx, db, query.Directives{}, query.Page{Number: 1, Size: 5})
	_, _ = p.List(ctx, db, query.Directives{Sort: []query.Sort{{Field: "id", Descending: true}}}, query.Page{Number: 1, Size: 5})
	_, _ = p.List(ctx, db, query.Directives{Sort: []query.Sort{{Field: "age"}, {Field: "name", Descending: true}}}, query.Page{Number: 3, Size: 5})
	_, _ = p.List(ctx, db, query.Directives{Sort: []query.Sort{{Field: "name", Descending: true}}}, query.Page{Number: 1, Size: 5})
	pages := rec.SQL(sqltest.OpQuery)
	for i, want := range []string{
		" ORDER BY q.id OFFSET $1 ROWS FETCH NEXT $2 ROWS ONLY",
		" ORDER BY q.id DESC OFFSET $1 ROWS FETCH NEXT $2 ROWS ONLY",
		// The sorts run in two directions, so the tie-breaker takes neither
		// and the ordering cannot be continued by a cursor.
		" ORDER BY q.age, q.name DESC, q.id OFFSET $1 ROWS FETCH NEXT $2 ROWS ONLY",
		// One direction across the sorts, so the tie-breaker takes it too.
		" ORDER BY q.name DESC, q.id DESC OFFSET $1 ROWS FETCH NEXT $2 ROWS ONLY",
	} {
		if got := pages[2*i+1]; !strings.HasSuffix(got, want) {
			t.Errorf("page %d = %q, want suffix %q", i, got, want)
		}
	}
	if a := rec.Calls()[5].Args; a[0] != 10 || a[1] != 6 {
		t.Errorf("page 3 of 5 bound %v, want offset 10, fetch 6", a)
	}
}

func TestList_DirectiveErrorsUnwrapToErrDirectivesBeforeAnyIO(t *testing.T) {
	db, rec := session(t)
	p := projection(t)
	ok := query.Page{Number: 1, Size: 5}
	cases := map[string]struct {
		d    query.Directives
		page query.Page
	}{
		"page 0":         {query.Directives{}, query.Page{Number: 0, Size: 5}},
		"size 0":         {query.Directives{}, query.Page{Number: 1, Size: 0}},
		"unknown filter": {query.Directives{Filters: []query.Filter{{Field: "email", Op: query.OpEq, Value: "x"}}}, ok},
		"unknown sort":   {query.Directives{Sort: []query.Sort{{Field: "email"}}}, ok},
		"unknown op":     {query.Directives{Filters: []query.Filter{{Field: "name", Op: "matches", Value: "x"}}}, ok},
		"in not a slice": {query.Directives{Filters: []query.Filter{{Field: "name", Op: query.OpIn, Value: "x"}}}, ok},
		"in empty":       {query.Directives{Filters: []query.Filter{{Field: "name", Op: query.OpIn, Value: []any{}}}}, ok},
		"total mode":     {query.Directives{Total: 7}, ok},
	}
	for name, c := range cases {
		_, err := p.List(context.Background(), db, c.d, c.page)
		if !errors.Is(err, query.ErrDirectives) {
			t.Errorf("%s: err = %v, want ErrDirectives", name, err)
		}
	}
	continued := map[string]struct {
		after query.Cursor
		size  int
	}{
		"no cursor": {"", 5},
		"size 0":    {"whatever", 0},
		// person_view declares no field not null, so no ordering over it can
		// be continued by a cursor.
		"cursor unsupported": {"whatever", 5},
	}
	for name, c := range continued {
		_, err := p.Continue(context.Background(), db, query.Directives{}, c.after, c.size)
		if !errors.Is(err, query.ErrDirectives) {
			t.Errorf("Continue %s: err = %v, want ErrDirectives", name, err)
		}
	}
	var unknown *query.UnknownFieldError
	_, err := p.List(context.Background(), db, cases["unknown sort"].d, ok)
	if !errors.As(err, &unknown) || unknown.Use != query.FieldUseSort {
		t.Errorf("unknown sort = %v", err)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("rejected declarations were sent to the driver: %v", rec.Ops())
	}
}

// dataException is a driver error that exposes an SQLSTATE, the shape an
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
	_, err := projection(t).List(context.Background(), db, query.Directives{
		Filters: []query.Filter{{Field: "id", Op: query.OpEq, Value: "nope"}},
	}, query.Page{Number: 1, Size: 5})
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
	fields := "SELECT q.id, q.name, q.age FROM (" + base + ") q WHERE q.id = CAST(NULL AS uuid) AND q.name = CAST(NULL AS text) AND q.age = CAST(NULL AS integer)"
	// The cursor page probes the keyset predicate and the paging clause, so
	// an engine's respelling of either is prepared at startup too.
	cursor := "SELECT * FROM (" + base + ") q WHERE (q.id > CAST($1 AS uuid)) ORDER BY q.id OFFSET $2 ROWS FETCH NEXT $3 ROWS ONLY"
	if got := rec.SQL(sqltest.OpPrepare); len(got) != 2 || got[0] != fields || got[1] != cursor {
		t.Errorf("probes = %q", got)
	}
	rec.FailPrepare = func(string) error { return errors.New(`column q.age does not exist`) }
	if err := projection(t).Verify(context.Background(), db); err == nil || !strings.Contains(err.Error(), "person_view: field contract") {
		t.Errorf("Verify = %v, want the base named", err)
	}
	rec.FailPrepare = func(q string) error {
		if strings.Contains(q, "ORDER BY") {
			return errors.New("syntax error near FETCH")
		}
		return nil
	}
	if err := projection(t).Verify(context.Background(), db); err == nil || !strings.Contains(err.Error(), "person_view: cursor page") {
		t.Errorf("Verify = %v, want the cursor page named", err)
	}
}

func TestProject_RequiresAContractAndNoExpandedParameter(t *testing.T) {
	stmts := catalog().MustCompile(projectionFiles, "sql", sqltest.Dialect{})
	for _, name := range []string{"no_contract", "expanded_param"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Project(%s) did not panic", name)
				}
			}()
			stmts.Statement(name).Project(scanPerson)
		}()
	}
	// A base that binds parameters of its own, none of them expanded, is
	// projectable: List, Continue, and One bind them from their base arguments.
	stmts.Statement("with_param").Project(scanPerson)
}

// tenantView is the projection over a base that binds one parameter of its
// own, so the request's placeholders are numbered after the base's.
func tenantView(t *testing.T, name string) query.Projection[string] {
	t.Helper()
	return catalog().MustCompile(projectionFiles, "sql", sqltest.Dialect{}).Statement(name).Project(query.Scalar[string])
}

func ids(values ...string) sqltest.Response {
	r := sqltest.Response{Columns: []string{"id"}}
	for _, v := range values {
		r.Rows = append(r.Rows, []driver.Value{v})
	}
	return r
}

func TestList_BindsTheBasesOwnParametersBeforeTheRequests(t *testing.T) {
	db, rec := session(t, count(1), ids("a"))
	got, err := tenantView(t, "with_param").List(context.Background(), db, query.Directives{
		Filters: []query.Filter{{Field: "id", Op: query.OpEq, Value: "a"}},
	}, query.Page{Number: 1, Size: 5}, query.With("tenant", "t1"))
	if err != nil || got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("List = %+v, %v", got, err)
	}
	tenant := "SELECT id FROM t WHERE tenant = $1"
	calls := rec.Calls()
	if calls[0].SQL != "SELECT COUNT(*) FROM ("+tenant+") q WHERE q.id = CAST($2 AS uuid)" {
		t.Errorf("count sql = %q", calls[0].SQL)
	}
	if a := calls[0].Args; len(a) != 2 || a[0] != "t1" || a[1] != "a" {
		t.Errorf("count args = %v, want the base's value then the filter's", a)
	}
	if calls[1].SQL != "SELECT * FROM ("+tenant+") q WHERE q.id = CAST($2 AS uuid) ORDER BY q.id OFFSET $3 ROWS FETCH NEXT $4 ROWS ONLY" {
		t.Errorf("page sql = %q", calls[1].SQL)
	}
	if a := calls[1].Args; len(a) != 4 || a[0] != "t1" || a[1] != "a" || a[2] != 0 || a[3] != 6 {
		t.Errorf("page args = %v", a)
	}
}

func TestOne_BindsTheBasesOwnParameters(t *testing.T) {
	db, rec := session(t, ids("a"))
	got, err := tenantView(t, "with_param").One(context.Background(), db, "id", "a", query.With("tenant", "t1"))
	if err != nil || got != "a" {
		t.Fatalf("One = %q, %v", got, err)
	}
	c := rec.Calls()[0]
	if c.SQL != "SELECT * FROM (SELECT id FROM t WHERE tenant = $1) q WHERE q.id = CAST($2 AS uuid)" {
		t.Errorf("sql = %q", c.SQL)
	}
	if a := c.Args; a[0] != "t1" || a[1] != "a" {
		t.Errorf("args = %v", a)
	}
}

func TestList_BaseArgumentsMergeLeftToRight(t *testing.T) {
	db, rec := session(t, count(0), sqltest.Response{Columns: []string{"id", "name"}})
	p := catalog().MustCompile(projectionFiles, "sql", sqltest.Dialect{}).Statement("two_params").Project(query.Scalar[string])
	d, page := query.Directives{}, query.Page{Number: 1, Size: 5}
	if _, err := p.List(context.Background(), db, d, page, query.With("tenant", "first").With("region", "eu"), query.With("tenant", "second")); err != nil {
		t.Fatal(err)
	}
	if a := rec.Calls()[0].Args; a[0] != "second" || a[1] != "eu" {
		t.Errorf("args = %v, want the later tenant and the earlier region", a)
	}
}

func TestList_MissingBaseArgumentIsAnArgumentErrorBeforeAnyIO(t *testing.T) {
	db, rec := session(t)
	p := tenantView(t, "with_param")
	d, page := query.Directives{}, query.Page{Number: 1, Size: 5}
	var missing *query.ArgumentError
	_, err := p.List(context.Background(), db, d, page)
	if !errors.As(err, &missing) || missing.Statement != "with_param" || missing.Name != "tenant" {
		t.Errorf("List = %v, want an ArgumentError naming the base and the parameter", err)
	}
	if _, err := p.One(context.Background(), db, "id", "a"); !errors.As(err, &missing) {
		t.Errorf("One = %v, want an ArgumentError", err)
	}
	if errors.Is(err, query.ErrDirectives) {
		t.Error("a missing base argument is the caller's defect, not a request error")
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("a missing base argument reached the driver: %v", rec.Ops())
	}
}

func TestList_TotalNoneSkipsTheCount(t *testing.T) {
	db, rec := session(t, people(2))
	got, err := projection(t).List(context.Background(), db, query.Directives{
		Total: query.TotalNone,
	}, query.Page{Number: 1, Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != query.NoTotal || len(got.Items) != 2 {
		t.Errorf("List = %+v, want %d rows and no total", got, 2)
	}
	if ops := rec.Ops(); len(ops) != 1 || ops[0] != sqltest.OpQuery {
		t.Errorf("ops = %v, want the page alone", ops)
	}
}

func TestList_MoreReportsTheRowPastThePage(t *testing.T) {
	// The page reads one row past its size: three rows for a page of two
	// means a further page, and the extra row is not scanned.
	db, rec := session(t, count(9), people(3))
	got, err := projection(t).List(context.Background(), db, query.Directives{}, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !got.More || len(got.Items) != 2 || got.Items[1].ID != "b" {
		t.Fatalf("List = %+v", got)
	}
	// person_view declares no field not null, so the page reports a further
	// page without issuing a cursor for it.
	if got.Next != "" {
		t.Errorf("next = %q, want none over a nullable key", got.Next)
	}
	if rec.RowsLeaked() != 0 {
		t.Errorf("the row past the page leaked the row set")
	}
}
