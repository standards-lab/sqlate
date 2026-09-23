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
	// A cursorable base that binds a parameter of its own.
	"sql/tenant_member.sql": {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid not null\nSELECT id FROM member WHERE tenant = {{tenant}}")},
}

func projection(t *testing.T) query.Projection[person] {
	t.Helper()
	return catalog().MustCompile(projectionFiles, "sql", sqltest.Dialect{}).Statement("person_view").Project(scanPerson)
}

// counted is the page text a List or Continue sends under TotalExact: the
// base filtered inside the window that counts it, then after, order, and
// paging outside it.
func counted(base, where, after, rest string) string {
	return "SELECT * FROM (SELECT q.*, COUNT(*) OVER () AS sqlate_total FROM (" + base + ") q" + where + ") q" + after + rest
}

func people(n int) sqltest.Response {
	r := sqltest.Response{Columns: []string{"id", "name", "age"}}
	for i := range n {
		r.Rows = append(r.Rows, []driver.Value{string(rune('a' + i)), "P", int64(20 + i)})
	}
	return r
}

func TestList_ComposesOneCountedPageOverTheBase(t *testing.T) {
	db, rec := session(t, sqltest.WithTotal(people(2), 12))
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
	// The total and the page are one statement, so they read one snapshot.
	calls := rec.Calls()
	if len(calls) != 1 {
		t.Fatalf("ops = %v, want the counted page alone", rec.Ops())
	}
	if want := counted(base, " WHERE q.age >= CAST($1 AS integer)", "", " ORDER BY q.name DESC, q.id DESC OFFSET $2 ROWS FETCH NEXT $3 ROWS ONLY"); calls[0].SQL != want {
		t.Errorf("page sql = %q", calls[0].SQL)
	}
	if a := calls[0].Args; len(a) != 3 || a[0] != "21" || a[1] != 10 || a[2] != 11 {
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
			db, rec := session(t, sqltest.WithTotal(people(0), 0))
			_, err := projection(t).List(context.Background(), db, query.Directives{
				Filters: []query.Filter{{Field: "name", Op: op, Value: c.value}},
			}, query.Page{Number: 1, Size: 5})
			if err != nil {
				t.Fatal(err)
			}
			page := rec.Calls()[0]
			if !strings.HasPrefix(page.SQL, counted(base, " WHERE "+c.want, "", " ORDER BY")) {
				t.Errorf("sql = %q", page.SQL)
			}
			// The filter's values, then the paging bounds.
			if len(page.Args) != len(c.args)+2 {
				t.Errorf("args = %v, want %v then the paging bounds", page.Args, c.args)
			}
		})
	}
}

func TestList_KeyIsTheTieBreakerUnlessSortedBy(t *testing.T) {
	empty := sqltest.WithTotal(people(0), 0)
	db, rec := session(t, empty, empty, empty, empty)
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
		if got := pages[i]; !strings.HasSuffix(got, want) {
			t.Errorf("page %d = %q, want suffix %q", i, got, want)
		}
	}
	if a := rec.Calls()[2].Args; a[0] != 10 || a[1] != 6 {
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
	// The counted cursor page probes the window's layering with them.
	countedCursor := counted(base, "", " WHERE (q.id > CAST($1 AS uuid))", " ORDER BY q.id OFFSET $2 ROWS FETCH NEXT $3 ROWS ONLY")
	if got := rec.SQL(sqltest.OpPrepare); len(got) != 3 || got[0] != fields || got[1] != cursor || got[2] != countedCursor {
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
	rec.FailPrepare = func(q string) error {
		if strings.Contains(q, "OVER ()") {
			return errors.New("window functions are not allowed here")
		}
		return nil
	}
	if err := projection(t).Verify(context.Background(), db); err == nil || !strings.Contains(err.Error(), "person_view: counted cursor page: ") || !strings.Contains(err.Error(), "window functions") {
		t.Errorf("Verify = %v, want the counted cursor page named", err)
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

func TestProject_RefusesTheReservedTotalName(t *testing.T) {
	for _, name := range []string{"sqlate_total"} {
		stmts := catalog().MustCompile(fstest.MapFS{
			"sql/v.sql": {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid\n--| field: " + name + " integer\nSELECT id, 1 AS " + name + " FROM t")},
		}, "sql", sqltest.Dialect{})
		func() {
			defer func() {
				r := recover()
				if msg, _ := r.(string); !strings.Contains(msg, "v: field \""+name+"\" is the name the library reserves") {
					t.Errorf("Project with a field %s: recover = %v", name, r)
				}
			}()
			stmts.Statement("v").Project(query.Scalar[string])
		}()
	}
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
	db, rec := session(t, sqltest.WithTotal(ids("a"), 1))
	got, err := tenantView(t, "with_param").List(context.Background(), db, query.Directives{
		Filters: []query.Filter{{Field: "id", Op: query.OpEq, Value: "a"}},
	}, query.Page{Number: 1, Size: 5}, query.With("tenant", "t1"))
	if err != nil || got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("List = %+v, %v", got, err)
	}
	tenant := "SELECT id FROM t WHERE tenant = $1"
	calls := rec.Calls()
	if want := counted(tenant, " WHERE q.id = CAST($2 AS uuid)", "", " ORDER BY q.id OFFSET $3 ROWS FETCH NEXT $4 ROWS ONLY"); calls[0].SQL != want {
		t.Errorf("page sql = %q", calls[0].SQL)
	}
	if a := calls[0].Args; len(a) != 4 || a[0] != "t1" || a[1] != "a" || a[2] != 0 || a[3] != 6 {
		t.Errorf("page args = %v, want the base's value, the filter's, then the paging bounds", a)
	}

	// Continued, the keyset predicate sits outside the window and its value
	// binds after the filter's and before the paging bounds: the text's
	// order, which a dialect with positional placeholders reads.
	view := tenantView(t, "tenant_member")
	notZ := []query.Filter{{Field: "id", Op: query.OpNe, Value: "z"}}
	db, _ = session(t, sqltest.WithTotal(ids("a", "b"), 2))
	first, err := view.List(context.Background(), db, query.Directives{Filters: notZ}, query.Page{Number: 1, Size: 1}, query.With("tenant", "t1"))
	if err != nil || first.Next == "" {
		t.Fatalf("List = %+v, %v; want a cursor", first, err)
	}
	db, rec = session(t, sqltest.WithTotal(ids("b"), 2))
	next, err := view.Continue(context.Background(), db, query.Directives{Filters: notZ}, first.Next, 1, query.With("tenant", "t1"))
	if err != nil || next.Total != 2 || len(next.Items) != 1 || next.Items[0] != "b" {
		t.Fatalf("Continue = %+v, %v", next, err)
	}
	members := "SELECT id FROM member WHERE tenant = $1"
	c := rec.Calls()[0]
	if want := counted(members, " WHERE q.id <> CAST($2 AS uuid)", " WHERE (q.id > CAST($3 AS uuid))", " ORDER BY q.id OFFSET $4 ROWS FETCH NEXT $5 ROWS ONLY"); c.SQL != want {
		t.Errorf("continued sql = %q", c.SQL)
	}
	if a := c.Args; len(a) != 5 || a[0] != "t1" || a[1] != "z" || a[2] != "a" || a[3] != 0 || a[4] != 2 {
		t.Errorf("continued args = %v, want the base's, the filter's, the cursor's, then the paging bounds", a)
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
	db, rec := session(t, sqltest.WithTotal(sqltest.Response{Columns: []string{"id", "name"}}, 0))
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
	if got := rec.Calls()[0].SQL; got != "SELECT * FROM ("+base+") q ORDER BY q.id OFFSET $1 ROWS FETCH NEXT $2 ROWS ONLY" {
		t.Errorf("sql = %q, want the plain page", got)
	}
}

func TestList_MoreReportsTheRowPastThePage(t *testing.T) {
	// The page reads one row past its size: three rows for a page of two
	// means a further page, and the extra row is not scanned.
	db, rec := session(t, sqltest.WithTotal(people(3), 9))
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

func TestList_ScannerNeverSeesTheTotalColumn(t *testing.T) {
	type row struct {
		ID   string `db:"id"`
		Name string `db:"name"`
		Age  int64  `db:"age"`
	}
	db, _ := session(t, sqltest.WithTotal(people(2), 2))
	view := catalog().MustCompile(projectionFiles, "sql", sqltest.Dialect{}).Statement("person_view").Project(query.Scanner[row]())
	// Scanner refuses a column it has no field for, so a page it reads
	// shows it only the base's columns.
	got, err := view.List(context.Background(), db, query.Directives{}, query.Page{Number: 1, Size: 5})
	if err != nil || got.Total != 2 || len(got.Items) != 2 || got.Items[1].Age != 21 {
		t.Fatalf("List = %+v, %v", got, err)
	}
}

func TestList_TheScansDestinationsAreLeftAsGiven(t *testing.T) {
	var spare []any
	scan := func(rows query.Row) (person, error) {
		var p person
		// Spare capacity past the destinations: the count must not be
		// appended into the caller's backing array.
		dest := make([]any, 3, 4)
		dest[0], dest[1], dest[2] = &p.ID, &p.Name, &p.Age
		err := rows.Scan(dest...)
		spare = dest[:4]
		return p, err
	}
	db, _ := session(t, sqltest.WithTotal(people(1), 1))
	view := catalog().MustCompile(projectionFiles, "sql", sqltest.Dialect{}).Statement("person_view").Project(scan)
	got, err := view.List(context.Background(), db, query.Directives{}, query.Page{Number: 1, Size: 5})
	if err != nil || got.Total != 1 || got.Items[0].Age != 20 {
		t.Fatalf("List = %+v, %v", got, err)
	}
	if spare[3] != nil {
		t.Errorf("the count was written into the caller's slice: %v", spare[3])
	}
}

func TestList_ReadsTheTotalFromWithTotalsColumn(t *testing.T) {
	// sqltest.WithTotal keeps its own copy of the reserved name; a counted
	// read checks the last column's name, so a copy that drifted would fail
	// here.
	db, _ := session(t, sqltest.WithTotal(people(2), 7))
	got, err := projection(t).List(context.Background(), db, query.Directives{}, query.Page{Number: 1, Size: 5})
	if err != nil || got.Total != 7 || len(got.Items) != 2 {
		t.Errorf("List over WithTotal = %+v, %v; want 2 items of 7", got, err)
	}
}

func TestList_RefusesACountedPageWithoutItsTotalColumnLast(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		page sqltest.Response
		want string
	}{
		{
			// An overlay that moved the count: the last column would be
			// hidden from the scan and read as the total.
			name: "count not last",
			page: sqltest.Response{Columns: []string{"id", "sqlate_total", "name", "age"}, Rows: [][]driver.Value{{"a", int64(1), "P", int64(20)}}},
			want: `query: person_view: a counted page's last column is "age", not sqlate_total`,
		},
		{
			name: "no count",
			page: people(1),
			want: `query: person_view: a counted page's last column is "age", not sqlate_total`,
		},
		{
			// A base that outputs the reserved name without declaring it.
			name: "base outputs the name",
			page: sqltest.WithTotal(sqltest.Response{Columns: []string{"id", "name", "age", "SQLATE_TOTAL"}, Rows: [][]driver.Value{{"a", "P", int64(20), int64(5)}}}, 1),
			want: "query: person_view: the base outputs a column named sqlate_total, which the library reserves",
		},
	}
	for _, c := range cases {
		db, rec := session(t, c.page)
		_, err := projection(t).List(ctx, db, query.Directives{}, query.Page{Number: 1, Size: 5})
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: List err = %v, want %q", c.name, err, c.want)
		}
		if rec.RowsLeaked() != 0 {
			t.Errorf("%s: the refused page leaked its row set", c.name)
		}
	}
	// Continue reads through the same check, naming its own base.
	db, _ := session(t, members("c"))
	_, err := memberView(t).Continue(ctx, db, query.Directives{}, issue(t, nil), 2)
	if want := `query: member: a counted page's last column is "name", not sqlate_total`; err == nil || err.Error() != want {
		t.Errorf("Continue err = %v, want %q", err, want)
	}
}

func TestList_AWrongDestinationCountNamesTheVisibleColumns(t *testing.T) {
	short := func(rows query.Row) (person, error) {
		var p person
		return p, rows.Scan(&p.ID, &p.Name)
	}
	view := catalog().MustCompile(projectionFiles, "sql", sqltest.Dialect{}).Statement("person_view").Project(short)
	db, _ := session(t, sqltest.WithTotal(people(1), 1))
	_, err := view.List(context.Background(), db, query.Directives{}, query.Page{Number: 1, Size: 5})
	// The same words database/sql uses over an uncounted page, with the
	// count column left out of both sides.
	if want := "sql: expected 3 destination arguments in Scan, not 2"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want %q", err, want)
	}
	db, _ = session(t, people(1))
	_, uncounted := view.List(context.Background(), db, query.Directives{Total: query.TotalNone}, query.Page{Number: 1, Size: 5})
	if err == nil || uncounted == nil || err.Error() != uncounted.Error() {
		t.Errorf("counted err = %v, uncounted err = %v; want the same", err, uncounted)
	}
}

func TestList_AScanThatDoesNotReadItsRowIsAnError(t *testing.T) {
	lazy := func(query.Row) (person, error) { return person{}, nil }
	view := catalog().MustCompile(projectionFiles, "sql", sqltest.Dialect{}).Statement("person_view").Project(lazy)
	db, rec := session(t, sqltest.WithTotal(people(2), 2))
	_, err := view.List(context.Background(), db, query.Directives{}, query.Page{Number: 1, Size: 5})
	if err == nil || err.Error() != "query: person_view: the scan did not read its row, so the page's total is unread" {
		t.Errorf("err = %v", err)
	}
	if rec.RowsLeaked() != 0 {
		t.Error("the failed page leaked its row set")
	}
	// Without the count there is nothing to read, and the scan is its own
	// business.
	db, _ = session(t, people(2))
	if _, err := view.List(context.Background(), db, query.Directives{Total: query.TotalNone}, query.Page{Number: 1, Size: 5}); err != nil {
		t.Errorf("TotalNone = %v", err)
	}
}

func TestList_AnEmptyPageCarriesATotalOnlyOnTheFirstPage(t *testing.T) {
	p := projection(t)
	ctx := context.Background()
	// Nothing under the filters: the first page is empty and the total is 0.
	db, _ := session(t, sqltest.WithTotal(people(0), 0))
	got, err := p.List(ctx, db, query.Directives{}, query.Page{Number: 1, Size: 5})
	if err != nil || got.Total != 0 || len(got.Items) != 0 || got.More {
		t.Errorf("empty first page = %+v, %v; want total 0", got, err)
	}
	// Past the end, no row carries the count.
	db, _ = session(t, sqltest.WithTotal(people(0), 0))
	got, err = p.List(ctx, db, query.Directives{}, query.Page{Number: 3, Size: 5})
	if err != nil || got.Total != query.NoTotal || len(got.Items) != 0 || got.More {
		t.Errorf("page past the end = %+v, %v; want NoTotal", got, err)
	}
	c := issue(t, nil)
	db, _ = session(t, sqltest.WithTotal(members(), 0))
	continued, err := memberView(t).Continue(ctx, db, query.Directives{}, c, 2)
	if err != nil || continued.Total != query.NoTotal || len(continued.Items) != 0 || continued.More {
		t.Errorf("empty continued page = %+v, %v; want NoTotal", continued, err)
	}
}

func TestList_TheTotalAgreesWithThePage(t *testing.T) {
	// Every page of a result of n rows, as an engine returns it: the rows
	// from the offset, one past the size, each carrying n. Whenever a page
	// reports a total, its items end at or before it and More says whether
	// they end before it.
	const size = 2
	for n := range 6 {
		table := people(n)
		for number := 1; number <= n/size+2; number++ {
			offset := (number - 1) * size
			page := sqltest.Response{Columns: table.Columns}
			if offset < n {
				page.Rows = table.Rows[offset:min(offset+size+1, n)]
			}
			db, _ := session(t, sqltest.WithTotal(page, int64(n)))
			got, err := projection(t).List(context.Background(), db, query.Directives{}, query.Page{Number: number, Size: size})
			if err != nil {
				t.Fatal(err)
			}
			if got.Total == query.NoTotal {
				if len(got.Items) != 0 || number == 1 {
					t.Errorf("n=%d page %d: no total over %+v", n, number, got)
				}
				continue
			}
			end := offset + len(got.Items)
			if got.Total != n || end > got.Total || got.More != (end < got.Total) {
				t.Errorf("n=%d page %d: %+v disagrees with its total", n, number, got)
			}
		}
	}
}
