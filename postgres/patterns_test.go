package postgres_test

import (
	"context"
	"database/sql/driver"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

// The engine's patterns are the library's with the keyset predicate alone
// respelled: every other pattern is accepted as written.
func TestPatterns_RespellOnlyTheKeysetPredicate(t *testing.T) {
	library := query.MustCatalog(query.Patterns()).Patterns()
	engine := query.MustCatalog(postgres.Patterns()).Patterns()
	if len(engine) != len(library) {
		t.Fatalf("the engine's inventory has %d patterns, the library's %d", len(engine), len(library))
	}
	for i, lib := range library {
		pg := engine[i]
		if pg.Namespace != lib.Namespace || pg.Name != lib.Name {
			t.Fatalf("pattern %d: %s.%s, want %s.%s", i, pg.Namespace, pg.Name, lib.Namespace, lib.Name)
		}
		if respelled := pg.Text != lib.Text; respelled != (lib.Name == "keyset") {
			t.Errorf("%s.%s respelled = %v: %q", pg.Namespace, pg.Name, respelled, pg.Text)
		}
	}
}

// A cursor issued by one page continues on the next through the overlay's
// row-value comparison, each keyed value bound once.
func TestPatterns_CursorContinuesWithARowValueComparison(t *testing.T) {
	view := query.MustCatalog(postgres.Patterns()).MustCompile(fstest.MapFS{
		"sql/member.sql": {Data: []byte("--| tier: standard\n--| key: org, id\n--| field: org uuid not null\n--| field: id uuid not null\nSELECT org, id FROM member")},
	}, "sql", postgres.Dialect{}).Statement("member").Project(func(rows query.Row) (string, error) {
		var org, id string
		err := rows.Scan(&org, &id)
		return id, err
	})
	rows := func(ids ...string) sqltest.Response {
		r := sqltest.Response{Columns: []string{"org", "id"}}
		for _, id := range ids {
			r.Rows = append(r.Rows, []driver.Value{"o", id})
		}
		return sqltest.WithTotal(r, 9)
	}

	pool, _ := sqltest.Open(t, rows("a", "b", "c"))
	first, err := view.List(context.Background(), sqlate.Wrap(pool, postgres.Dialect{}), query.Directives{}, query.Page{Number: 1, Size: 2})
	if err != nil || first.Next == "" {
		t.Fatalf("first page = %+v, %v; want a cursor", first, err)
	}

	pool, rec := sqltest.Open(t, rows("c"))
	if _, err := view.Continue(context.Background(), sqlate.Wrap(pool, postgres.Dialect{}), query.Directives{}, first.Next, 2); err != nil {
		t.Fatal(err)
	}
	want := "SELECT * FROM (SELECT q.*, COUNT(*) OVER () AS sqlate_total FROM (SELECT org, id FROM member) q) q WHERE (q.org, q.id) > (CAST($1 AS uuid), CAST($2 AS uuid)) ORDER BY q.org, q.id OFFSET $3 ROWS FETCH NEXT $4 ROWS ONLY"
	if got := rec.Calls()[0].SQL; got != want {
		t.Errorf("page sql = %q", got)
	}
	if a := rec.Calls()[0].Args; len(a) != 4 || a[0] != "o" || a[1] != "b" {
		t.Errorf("page args = %v", a)
	}
}
