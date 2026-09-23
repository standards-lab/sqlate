package query_test

import (
	"context"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

// member is the row of the composite-key base, whose key fields are both
// declared not null, so its orderings can be continued by a cursor.
type member struct {
	Org  string
	ID   string
	Name string
}

func scanMember(rows query.Row) (member, error) {
	var m member
	err := rows.Scan(&m.Org, &m.ID, &m.Name)
	return m, err
}

const memberBase = "SELECT org, id, name FROM member"

func members(values ...driver.Value) sqltest.Response {
	r := sqltest.Response{Columns: []string{"org", "id", "name"}}
	for _, v := range values {
		r.Rows = append(r.Rows, []driver.Value{"o", v, "M"})
	}
	return r
}

func memberView(t *testing.T) query.Projection[member] {
	t.Helper()
	return catalog().MustCompile(projectionFiles, "sql", sqltest.Dialect{}).Statement("member").Project(scanMember)
}

// issue lists one page over the member base and returns the cursor it
// issued, failing the test when it issued none.
func issue(t *testing.T, sorts []query.Sort, filters ...query.Filter) query.Cursor {
	t.Helper()
	db, _ := session(t, count(9), members("a", "b", "c"))
	got, err := memberView(t).List(context.Background(), db, query.Directives{Sort: sorts, Filters: filters}, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !got.More || got.Next == "" {
		t.Fatalf("List = %+v, want a further page and a cursor", got)
	}
	return got.Next
}

func TestContinue_ContinuesFromThePagesLastItem(t *testing.T) {
	c := issue(t, nil)
	// The cursor's page takes no page number: the keyset predicate stands in
	// for the offset, which stays 0.
	db, rec := session(t, count(9), members("c"))
	got, err := memberView(t).Continue(context.Background(), db, query.Directives{}, c, 2)
	if err != nil {
		t.Fatalf("Continue = %+v, %v", got, err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "c" || got.More || got.Next != "" {
		t.Errorf("continued page = %+v", got)
	}
	keyset := "(q.org > CAST($1 AS uuid) OR (q.org = CAST($1 AS uuid) AND q.id > CAST($2 AS uuid)))"
	calls := rec.Calls()
	// The count runs under the request's filters alone, so the cursor's
	// values are bound for the page only.
	if calls[0].SQL != "SELECT COUNT(*) FROM ("+memberBase+") q" || len(calls[0].Args) != 0 {
		t.Errorf("count = %+v", calls[0])
	}
	if calls[1].SQL != "SELECT * FROM ("+memberBase+") q WHERE "+keyset+" ORDER BY q.org, q.id OFFSET $3 ROWS FETCH NEXT $4 ROWS ONLY" {
		t.Errorf("page sql = %q", calls[1].SQL)
	}
	// The page's last item was ("o", "b"): each keyed value is bound once,
	// and the disjunct that repeats it reuses its placeholder.
	if a := calls[1].Args; len(a) != 4 || a[0] != "o" || a[1] != "b" || a[2] != 0 || a[3] != 3 {
		t.Errorf("page args = %v", a)
	}
}

func TestContinue_ADescendingOrderingComparesTheOtherWay(t *testing.T) {
	sorts := []query.Sort{{Field: "org", Descending: true}, {Field: "id", Descending: true}}
	c := issue(t, sorts)
	db, rec := session(t, count(9), members("c"))
	if _, err := memberView(t).Continue(context.Background(), db, query.Directives{Sort: sorts}, c, 2); err != nil {
		t.Fatal(err)
	}
	keyset := "(q.org < CAST($1 AS uuid) OR (q.org = CAST($1 AS uuid) AND q.id < CAST($2 AS uuid)))"
	want := "SELECT * FROM (" + memberBase + ") q WHERE " + keyset + " ORDER BY q.org DESC, q.id DESC OFFSET $3 ROWS FETCH NEXT $4 ROWS ONLY"
	if got := rec.Calls()[1].SQL; got != want {
		t.Errorf("page sql = %q", got)
	}
}

func TestContinue_CursorIsBoundAfterTheRequestsFilters(t *testing.T) {
	byName := query.Filter{Field: "name", Op: query.OpEq, Value: "M"}
	c := issue(t, nil, byName)
	db, rec := session(t, count(9), members("c"))
	_, err := memberView(t).Continue(context.Background(), db, query.Directives{
		Filters: []query.Filter{byName},
	}, c, 2)
	if err != nil {
		t.Fatal(err)
	}
	calls := rec.Calls()
	if calls[0].SQL != "SELECT COUNT(*) FROM ("+memberBase+") q WHERE q.name = CAST($1 AS text)" {
		t.Errorf("count sql = %q", calls[0].SQL)
	}
	keyset := "(q.org > CAST($2 AS uuid) OR (q.org = CAST($2 AS uuid) AND q.id > CAST($3 AS uuid)))"
	if got := calls[1].SQL; !strings.Contains(got, "WHERE q.name = CAST($1 AS text) AND "+keyset) {
		t.Errorf("page sql = %q", got)
	}
	if a := calls[1].Args; len(a) != 5 || a[0] != "M" || a[1] != "o" || a[2] != "b" || a[3] != 0 || a[4] != 3 {
		t.Errorf("page args = %v, want the filter, the cursor, then the paging bounds", a)
	}
}

func TestContinue_RefusesAnotherOrderingOrAnotherBase(t *testing.T) {
	ascending := issue(t, nil)
	descending := []query.Sort{{Field: "org", Descending: true}, {Field: "id", Descending: true}}
	cases := map[string]struct {
		sorts []query.Sort
		after query.Cursor
	}{
		"another direction": {descending, ascending},
		// Sorting by the second key field first puts the keyed prefix in the
		// other order, which the cursor records by name.
		"another prefix": {[]query.Sort{{Field: "id"}}, ascending},
	}
	for name, c := range cases {
		db, _ := session(t)
		_, err := memberView(t).Continue(context.Background(), db, query.Directives{Sort: c.sorts}, c.after, 2)
		var cursor *query.CursorError
		if !errors.As(err, &cursor) || cursor.Reason != query.CursorMismatch {
			t.Errorf("%s: err = %v, want a mismatch", name, err)
		}
		if !errors.Is(err, query.ErrDirectives) {
			t.Errorf("%s: err does not unwrap to ErrDirectives", name)
		}
	}

	// The same key fields and types under another base: the cursor names the
	// base it was issued for, so it is refused by name.
	other := catalog().MustCompile(fstest.MapFS{
		"sql/archive.sql": {Data: []byte("--| tier: standard\n--| key: org, id\n--| field: org uuid not null\n--| field: id uuid not null\nSELECT org, id FROM archive")},
	}, "sql", sqltest.Dialect{}).Statement("archive").Project(query.Scalar[string])
	db, _ := session(t)
	_, err := other.Continue(context.Background(), db, query.Directives{}, ascending, 2)
	var cursor *query.CursorError
	if !errors.As(err, &cursor) || cursor.Reason != query.CursorMismatch {
		t.Errorf("another base = %v, want a mismatch", err)
	}
}

func TestContinue_RefusesOtherFilters(t *testing.T) {
	c := issue(t, nil, query.Filter{Field: "name", Op: query.OpEq, Value: "M"})
	cases := map[string][]query.Filter{
		"no filters":     nil,
		"another value":  {{Field: "name", Op: query.OpEq, Value: "N"}},
		"another op":     {{Field: "name", Op: query.OpNe, Value: "M"}},
		"another filter": {{Field: "name", Op: query.OpEq, Value: "M"}, {Field: "org", Op: query.OpEq, Value: "o"}},
		// A value with no JSON form cannot be shown to match.
		"unrepresentable": {{Field: "name", Op: query.OpEq, Value: math.NaN()}},
	}
	for name, filters := range cases {
		db, rec := session(t)
		_, err := memberView(t).Continue(context.Background(), db, query.Directives{Filters: filters}, c, 2)
		var cursor *query.CursorError
		if !errors.As(err, &cursor) || cursor.Reason != query.CursorMismatch {
			t.Errorf("%s: err = %v, want a mismatch", name, err)
		}
		if len(rec.Calls()) != 0 {
			t.Errorf("%s: the refused cursor reached the driver: %v", name, rec.Ops())
		}
	}

	// The same filters rebuilt as a new slice continue: the cursor compares
	// their content, not their identity.
	db, _ := session(t, count(9), members("c"))
	same := []query.Filter{{Field: "name", Op: query.OpEq, Value: "M"}}
	if _, err := memberView(t).Continue(context.Background(), db, query.Directives{Filters: same}, c, 2); err != nil {
		t.Errorf("the same filters = %v, want the page", err)
	}

	// No filters and an empty filter slice are the same request.
	unfiltered := issue(t, nil)
	db, _ = session(t, count(9), members("c"))
	if _, err := memberView(t).Continue(context.Background(), db, query.Directives{Filters: []query.Filter{}}, unfiltered, 2); err != nil {
		t.Errorf("an empty filter slice after none = %v, want the page", err)
	}
}

func TestList_FiltersWithNoSignatureIssueNoCursor(t *testing.T) {
	// A NaN has no JSON form, so the filters cannot be bound into a cursor:
	// the page still reports that a further page exists, without one.
	db, _ := session(t, count(9), members("a", "b", "c"))
	got, err := memberView(t).List(context.Background(), db, query.Directives{
		Filters: []query.Filter{{Field: "name", Op: query.OpEq, Value: math.NaN()}},
	}, query.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || !got.More || got.Next != "" {
		t.Errorf("List = %+v, want two items, a further page, and no cursor", got)
	}
}

func TestContinue_RefusesAnOrderingItCannotContinue(t *testing.T) {
	c := issue(t, nil)
	// A nullable field in the keyed prefix: the keyset predicate cannot
	// compare it, so the sort cannot be continued.
	nullable := []query.Sort{{Field: "name"}, {Field: "org"}, {Field: "id"}}
	// Two directions across the keyed prefix: one comparison cannot serve
	// both.
	mixed := []query.Sort{{Field: "org"}, {Field: "id", Descending: true}}
	for name, sorts := range map[string][]query.Sort{"nullable field": nullable, "mixed directions": mixed} {
		db, rec := session(t)
		_, err := memberView(t).Continue(context.Background(), db, query.Directives{Sort: sorts}, c, 2)
		var cursor *query.CursorError
		if !errors.As(err, &cursor) || cursor.Reason != query.CursorUnsupported {
			t.Errorf("%s: err = %v, want unsupported", name, err)
		}
		if len(rec.Calls()) != 0 {
			t.Errorf("%s: the refused cursor reached the driver: %v", name, rec.Ops())
		}
	}
}

func TestContinue_RefusesTextItDidNotIssue(t *testing.T) {
	issued := issue(t, nil)
	raw, err := base64.RawURLEncoding.DecodeString(string(issued))
	if err != nil {
		t.Fatal(err)
	}
	// The body still decodes and still names this base, this prefix, and this
	// direction; only a value changed, which the check prefix catches.
	tampered := strings.Replace(string(raw[4:]), `"b"`, `"z"`, 1)
	if tampered == string(raw[4:]) || !json.Valid([]byte(tampered)) {
		t.Fatalf("the cursor body did not tamper into another valid body: %q", raw[4:])
	}
	corrupt := query.Cursor(base64.RawURLEncoding.EncodeToString(append(raw[:4:4], tampered...)))

	// A type change under the same base, prefix, and direction: the keyed
	// fields' declared types are hashed with the body, not carried in it.
	retyped := catalog().MustCompile(fstest.MapFS{
		"sql/member.sql": {Data: []byte("--| tier: standard\n--| key: org, id\n--| field: org uuid not null\n--| field: id text not null\n--| field: name text\n" + memberBase)},
	}, "sql", sqltest.Dialect{}).Statement("member").Project(scanMember)

	cases := map[string]struct {
		p     query.Projection[member]
		after query.Cursor
	}{
		"not base64":     {memberView(t), "!! not base64 !!"},
		"too short":      {memberView(t), query.Cursor(base64.RawURLEncoding.EncodeToString([]byte{1, 2}))},
		"not json":       {memberView(t), query.Cursor(base64.RawURLEncoding.EncodeToString([]byte("abcd not json")))},
		"tampered value": {memberView(t), corrupt},
		"retyped field":  {retyped, issued},
	}
	for name, c := range cases {
		db, rec := session(t)
		_, err := c.p.Continue(context.Background(), db, query.Directives{}, c.after, 2)
		var cursor *query.CursorError
		if !errors.As(err, &cursor) || cursor.Reason != query.CursorMalformed {
			t.Errorf("%s: err = %v, want malformed", name, err)
		}
		if len(rec.Calls()) != 0 {
			t.Errorf("%s: the refused cursor reached the driver: %v", name, rec.Ops())
		}
	}
}

func TestList_KeyedColumnsAreMatchedInThePagesOwnCase(t *testing.T) {
	// An engine folds an unquoted alias's case, so the keyed field is found
	// case-insensitively when no column matches it exactly.
	folded := sqltest.Response{Columns: []string{"ORG", "ID", "NAME"}, Rows: [][]driver.Value{
		{"o", "a", "M"}, {"o", "b", "M"}, {"o", "c", "M"},
	}}
	db, _ := session(t, count(9), folded)
	got, err := memberView(t).List(context.Background(), db, query.Directives{}, query.Page{Number: 1, Size: 2})
	if err != nil || got.Next == "" {
		t.Fatalf("List = %+v, %v, want a cursor over the folded columns", got, err)
	}
}

func TestList_AKeyedFieldThePageDoesNotOutputIsTheContractsDefect(t *testing.T) {
	db, _ := session(t, count(9), sqltest.Response{Columns: []string{"id", "name"}, Rows: [][]driver.Value{{"a", "M"}}})
	_, err := memberView(t).List(context.Background(), db, query.Directives{}, query.Page{Number: 1, Size: 2})
	if err == nil || !strings.Contains(err.Error(), `keyed field "org" is not an output column`) {
		t.Errorf("err = %v, want the missing column named", err)
	}
}

func TestList_NullInAKeyedColumnIsTheBasesDefect(t *testing.T) {
	// The page's last row carries a null where the contract declared the
	// field not null, so no cursor can be issued from it.
	db, _ := session(t, count(9), members("a", nil, "c"))
	_, err := memberView(t).List(context.Background(), db, query.Directives{}, query.Page{Number: 1, Size: 2})
	if err == nil {
		t.Fatal("List did not fail")
	}
	if errors.Is(err, query.ErrDirectives) {
		t.Errorf("err = %v, want the base's defect and not a request error", err)
	}
	if !strings.Contains(err.Error(), `keyed field "id"`) || !strings.Contains(err.Error(), "is null") {
		t.Errorf("err = %v, want the field named", err)
	}
}
