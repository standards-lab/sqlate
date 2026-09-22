package postgres_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

// The overlay parses, declares the library keyset pattern's alternate slots,
// and replaces the pattern in the catalog.
func TestPatterns_OverlaysTheKeysetPredicate(t *testing.T) {
	c, err := query.NewCatalog(postgres.Patterns())
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(c.Patterns(), func(p query.Pattern) bool { return p.Namespace == query.Namespace && p.Name == "keyset" })
	if i < 0 {
		t.Fatal("no sql.keyset in the inventory")
	}
	p := c.Patterns()[i]
	if !slices.Equal(p.Slots, []string{"columns", "op", "values"}) || p.Tier != query.TierNative {
		t.Errorf("sql.keyset = %+v", p)
	}
}

// A cursor issued by one page continues on the next through the overlay's
// row-value comparison, each keyed value bound once.
func TestPatterns_CursorContinuesWithARowValueComparison(t *testing.T) {
	view := query.MustCatalog(postgres.Patterns()).MustCompile(fstest.MapFS{
		"sql/member.sql": {Data: []byte("--| tier: standard\n--| key: org, id\n--| field: org uuid not null\n--| field: id uuid not null\nSELECT org, id FROM member")},
	}, "sql", postgres.Dialect{}).Statement("member").Project(func(rows *sql.Rows) (string, error) {
		var org, id string
		err := rows.Scan(&org, &id)
		return id, err
	})
	count := sqltest.Response{Columns: []string{"count"}, Rows: [][]driver.Value{{int64(9)}}}
	rows := func(ids ...string) sqltest.Response {
		r := sqltest.Response{Columns: []string{"org", "id"}}
		for _, id := range ids {
			r.Rows = append(r.Rows, []driver.Value{"o", id})
		}
		return r
	}

	pool, _ := sqltest.Open(t, count, rows("a", "b", "c"))
	first, err := view.List(context.Background(), sqlate.Wrap(pool, postgres.Dialect{}), query.Directives{}, query.Page{Number: 1, Size: 2})
	if err != nil || first.Next == "" {
		t.Fatalf("first page = %+v, %v; want a cursor", first, err)
	}

	pool, rec := sqltest.Open(t, count, rows("c"))
	if _, err := view.Continue(context.Background(), sqlate.Wrap(pool, postgres.Dialect{}), query.Directives{}, first.Next, 2); err != nil {
		t.Fatal(err)
	}
	want := "SELECT * FROM (SELECT org, id FROM member) q WHERE (q.org, q.id) > (CAST($1 AS uuid), CAST($2 AS uuid)) ORDER BY q.org, q.id OFFSET $3 ROWS FETCH NEXT $4 ROWS ONLY"
	if got := rec.Calls()[1].SQL; got != want {
		t.Errorf("page sql = %q", got)
	}
	if a := rec.Calls()[1].Args; len(a) != 4 || a[0] != "o" || a[1] != "b" {
		t.Errorf("page args = %v", a)
	}
}
