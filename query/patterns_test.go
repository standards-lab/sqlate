package query_test

import (
	"context"
	"database/sql/driver"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

// A statement file includes the guard patterns; the spliced text binds the
// pattern's parameters like the file's own.
func TestLoad_ExpandsPatternIncludes(t *testing.T) {
	st := load(t, "--| tier: standard\nUPDATE t SET a = {{a}}, {{> sql.guard_set}}\nWHERE {{> sql.guard_where}}")
	if st.Text() != "UPDATE t SET a = $1, updated_at = CURRENT_TIMESTAMP, version = version + 1\nWHERE id = $2 AND version = $3" {
		t.Errorf("text = %q", st.Text())
	}
	if got := st.Params(); !slices.Equal(got, []string{"a", "id", "version"}) {
		t.Errorf("params = %v", got)
	}
}

func TestLoad_RejectsBadIncludes(t *testing.T) {
	cases := map[string]string{
		"unqualified":       "{{> guard_where}}",
		"unknown namespace": "{{> org.lineage}}",
		"unknown pattern":   "{{> sql.nope}}",
	}
	for name, inc := range cases {
		_, err := catalog().Compile(fstest.MapFS{"sql/s.sql": {Data: []byte("--| tier: standard\nSELECT 1 WHERE " + inc)}}, "sql", sqltest.Dialect{})
		if err == nil || !strings.Contains(err.Error(), "s.sql") {
			t.Errorf("%s: err = %v, want a load error naming the file", name, err)
		}
	}
}

// The guard runs over the included patterns exactly as over hand-written
// text.
func TestGuard_OverIncludedPatterns(t *testing.T) {
	stmts := catalog().MustCompile(fstest.MapFS{
		"sql/edit.sql":    {Data: []byte("--| tier: standard\nUPDATE t SET a = {{a}}, {{> sql.guard_set}} WHERE {{> sql.guard_where}}")},
		"sql/version.sql": {Data: []byte("--| tier: standard\nSELECT version FROM t WHERE id = {{id}}")},
	}, "sql", sqltest.Dialect{})
	db, rec := session(t, sqltest.Response{Affected: 1})
	v, err := stmts.Statement("edit").Guarded(stmts.Statement("version"), "version").Run(context.Background(), db, 4, query.Args{"id": "x", "a": 1})
	if err != nil || v != 5 {
		t.Fatalf("Run = %d, %v", v, err)
	}
	if c := rec.Calls()[0]; c.Args[1] != "x" || c.Args[2] != int64(4) {
		t.Errorf("bound %v", c.Args)
	}
}

// A port overlays the library's paging: the collection read composes with
// the port's spelling and the slots the library fills are unchanged.
func TestOverlay_RespellsPagingForAPort(t *testing.T) {
	mysql := fstest.MapFS{"port/paging.sql": {Data: []byte("--| tier: native\n--| native: mysql — LIMIT/OFFSET\n LIMIT {{fetch}} OFFSET {{offset}}")}}
	c := query.MustCatalog(query.Patterns().Overlay(mysql, "port"))
	view := c.MustCompile(fstest.MapFS{
		"sql/v.sql": {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid\nSELECT id FROM t")},
	}, "sql", sqltest.Dialect{}).Statement("v").Project(query.Scalar[string])
	db, rec := session(t, sqltest.Response{Columns: []string{"count"}, Rows: [][]driver.Value{{int64(0)}}}, sqltest.Response{Columns: []string{"id"}})
	if _, _, err := view.List(context.Background(), db, query.Directives{Page: query.Page{Number: 3, Size: 4}}); err != nil {
		t.Fatal(err)
	}
	// The library binds offset then fetch, whatever order the port's text
	// names them in.
	if got := rec.Calls()[1].SQL; got != "SELECT * FROM (SELECT id FROM t) q ORDER BY q.id LIMIT $2 OFFSET $1" {
		t.Errorf("sql = %q", got)
	}
	if got := rec.Calls()[1].Args; got[0] != 8 || got[1] != 4 {
		t.Errorf("args = %v", got)
	}
}

func TestNewCatalog_RejectsWhatItCannotCompose(t *testing.T) {
	app := fstest.MapFS{"p/identity.sql": {Data: []byte("--| tier: standard\nRETURNING id, version")}}
	cases := map[string][]query.Source{
		"registered twice":       {query.Patterns(), query.Publish("sql", app, "p")},
		"replaces no pattern":    {query.Patterns().Overlay(app, "p")},
		"declares slots [n]":     {query.Patterns().Overlay(fstest.MapFS{"o/paging.sql": {Data: []byte("--| tier: standard\nLIMIT {{n}}")}}, "o")},
		"not an identifier":      {query.Publish("my-app", app, "p")},
		"pattern nope.sql (app)": {query.Publish("app", fstest.MapFS{"p/nope.sql": {Data: []byte("SELECT 1")}}, "p")},
		"patterns app: open":     {query.Publish("app", app, "missing")},
	}
	for want, sources := range cases {
		_, err := query.NewCatalog(sources...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
	if len(query.MustCatalog(query.Patterns(), query.Publish("app", app, "p")).Namespaces()) != 2 {
		t.Error("two namespaces did not register")
	}
}

// An application publishes its namespace beside the library's, and a
// statement includes from both; an alias renames the library's.
func TestCompile_IncludesAcrossNamespacesAndAliases(t *testing.T) {
	app := query.Publish("app", fstest.MapFS{
		"p/identity.sql": {Data: []byte("--| tier: native\n--| native: postgres — RETURNING\nRETURNING id, version")},
	}, "p")
	files := fstest.MapFS{
		"sql/create.sql": {Data: []byte("--| tier: native\n--| native: postgres — RETURNING\nINSERT INTO t (a) VALUES ({{a}}) {{> app.identity}}")},
		"sql/edit.sql":   {Data: []byte("--| tier: standard\nUPDATE t SET a = {{a}}, {{> lib.guard_set}} WHERE {{> lib.guard_where}}")},
	}
	stmts := query.MustCatalog(query.Patterns().As("lib"), app).MustCompile(files, "sql", sqltest.Dialect{})
	if got := stmts.Statement("create").Text(); got != "INSERT INTO t (a) VALUES ($1) RETURNING id, version" {
		t.Errorf("create = %q", got)
	}
	if got := stmts.Statement("edit").Params(); !slices.Equal(got, []string{"a", "id", "version"}) {
		t.Errorf("edit params = %v", got)
	}
	if stmts.Statement("edit").Catalog() == nil {
		t.Error("Catalog() returned nil")
	}
	if _, err := query.MustCatalog(query.Patterns().As("lib"), app).Compile(fstest.MapFS{
		"sql/s.sql": {Data: []byte("--| tier: standard\nUPDATE t SET a = 1 WHERE {{> sql.guard_where}}")},
	}, "sql", sqltest.Dialect{}); err == nil || !strings.Contains(err.Error(), `unknown namespace "sql" (registered: app, lib)`) {
		t.Errorf("aliased include = %v", err)
	}
}

// A native pattern spliced into a standard-tier statement would hide the
// port; the compile refuses it.
func TestCompile_RefusesANativePatternInAStandardStatement(t *testing.T) {
	app := query.Publish("app", fstest.MapFS{"p/identity.sql": {Data: []byte("--| tier: native\n--| native: postgres — RETURNING\nRETURNING id")}}, "p")
	_, err := query.MustCatalog(query.Patterns(), app).Compile(fstest.MapFS{
		"sql/s.sql": {Data: []byte("--| tier: standard\nINSERT INTO t (a) VALUES (1) {{> app.identity}}")},
	}, "sql", sqltest.Dialect{})
	if err == nil || !strings.Contains(err.Error(), `native pattern "app.identity" in a standard-tier statement`) {
		t.Errorf("err = %v", err)
	}
}
