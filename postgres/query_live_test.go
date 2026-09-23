//go:build integration

// Live proofs for the projection and the guard against the compose
// PostgreSQL: the engine parses request values through the contract's
// declared types, a value it cannot read is an InvalidValueError, and the
// guard's three outcomes hold against real rows. `mise run integration`.
package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"
)

var liveFiles = fstest.MapFS{
	"sql/view.sql":    {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid\n--| field: name text\n--| field: n integer\n--| field: at timestamp\nSELECT id, name, n, at FROM live_q")},
	"sql/edit.sql":    {Data: []byte("--| tier: standard\nUPDATE live_q SET name = {{name}}, version = version + 1 WHERE id = {{id}} AND version = {{version}}")},
	"sql/version.sql": {Data: []byte("--| tier: standard\nSELECT version FROM live_q WHERE id = {{id}}")},
	"sql/publish.sql": {Data: []byte("--| tier: standard\n--| returning: row\nUPDATE live_q SET name = {{name}}, version = version + 1 WHERE id = {{id}} AND version = {{version}} AND n > 1")},
	"sql/row.sql":     {Data: []byte("--| tier: standard\nSELECT id, name, n, at, version FROM live_q WHERE id = {{id}}")},
	"sql/insert.sql":  {Data: []byte("--| tier: standard\nINSERT INTO live_q (name, n) VALUES ({{name}}, {{n}})")},
}

type row struct {
	ID, Name string
	N        int64
	At       sql.NullTime
}

func scanRow(rows query.Row) (row, error) {
	var r row
	err := rows.Scan(&r.ID, &r.Name, &r.N, &r.At)
	return r, err
}

// liveQ creates the proofs' table with three rows and drops it at cleanup.
func liveQ(t testing.TB, db *sqlate.DB) {
	t.Helper()
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS live_q")
	if _, err := db.ExecContext(ctx, "CREATE TABLE live_q (id uuid PRIMARY KEY DEFAULT uuidv7(), name text NOT NULL, n integer NOT NULL, at timestamp, version bigint NOT NULL DEFAULT 1)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS live_q") })
	if _, err := db.ExecContext(ctx, "INSERT INTO live_q (name, n, at) VALUES ('a', 1, '2026-01-01'), ('b', 2, NULL), ('c', 3, '2026-03-01')"); err != nil {
		t.Fatal(err)
	}
}

// liveStatements compiles the proofs' files over the engine's patterns.
func liveStatements(db *sqlate.DB) *query.Statements {
	return query.MustCatalog(postgres.Patterns()).MustCompile(liveFiles, "sql", db.Dialect())
}

func TestLive_ProjectionAndGuard(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	liveQ(t, db)
	stmts := liveStatements(db)
	view := stmts.Statement("view").Project(scanRow)
	if err := query.Verify(ctx, db, stmts, view); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// Request values arrive as text and the engine parses them by the
	// contract's types: "2" as integer, an RFC 3339 date as timestamp.
	c, err := view.List(ctx, db, query.Directives{
		Sort:    []query.Sort{{Field: "n", Descending: true}},
		Filters: []query.Filter{{Field: "n", Op: query.OpGe, Value: "2"}, {Field: "at", Op: query.OpIsNotNull}},
	}, query.Page{Number: 1, Size: 10})
	if err != nil || c.Total != 1 || len(c.Items) != 1 || c.Items[0].Name != "c" || c.More {
		t.Fatalf("List = %+v, %v", c, err)
	}
	c, err = view.List(ctx, db, query.Directives{
		Filters: []query.Filter{{Field: "at", Op: query.OpLt, Value: "2026-02-01T00:00:00Z"}, {Field: "name", Op: query.OpIn, Value: []any{"a", "b", "c"}}},
	}, query.Page{Number: 2, Size: 2})
	// A page past the end has no row to carry the count, so it reports
	// none rather than a count its statement never read.
	if err != nil || c.Total != query.NoTotal || len(c.Items) != 0 || c.More {
		t.Fatalf("page past the end: List = %+v, %v; want no items and NoTotal", c, err)
	}

	// A value the engine cannot read as the field's type is the request's
	// fault, classified through the dialect's class-22 mapping.
	for _, f := range []query.Filter{
		{Field: "id", Op: query.OpEq, Value: "not-a-uuid"},
		{Field: "n", Op: query.OpGt, Value: "many"},
		{Field: "at", Op: query.OpGe, Value: "not-a-date"},
	} {
		_, err := view.List(ctx, db, query.Directives{Filters: []query.Filter{f}}, query.Page{Number: 1, Size: 1})
		var invalid *query.InvalidValueError
		if !errors.As(err, &invalid) || !errors.Is(err, query.ErrDirectives) {
			t.Errorf("%s %v: err = %v, want InvalidValueError", f.Field, f.Value, err)
		} else {
			t.Logf("%s=%v → %v", f.Field, f.Value, err)
		}
	}

	one, err := view.One(ctx, db, "name", "b")
	if err != nil || one.N != 2 || one.At.Valid {
		t.Fatalf("One = %+v, %v", one, err)
	}
	if _, err := view.One(ctx, db, "name", "zzz"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("One miss = %v", err)
	}

	edit := stmts.Statement("edit").Guarded(stmts.Statement("version"), "version")
	v, err := edit.Run(ctx, db, 1, query.Args{"id": one.ID, "name": "B"})
	if err != nil || v != 2 {
		t.Fatalf("guard hit = %d, %v", v, err)
	}
	_, err = edit.Run(ctx, db, 1, query.Args{"id": one.ID, "name": "B2"})
	if !errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "expected 1, current 2") {
		t.Errorf("stale guard = %v", err)
	}
	if _, err := edit.Run(ctx, db, 1, query.Args{"id": "00000000-0000-0000-0000-000000000000", "name": "x"}); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing row = %v", err)
	}
	if got, _ := view.One(ctx, db, "id", one.ID); got.Name != "B" {
		t.Errorf("the hit did not persist: %+v", got)
	}
}

var pageFiles = fstest.MapFS{
	"sql/pages.sql": {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid not null\n--| field: grp text not null\n--| field: n integer not null\nSELECT id, grp, n FROM live_page")},
}

type pageRow struct {
	ID, Grp string
	N       int64
}

// recorder is the session the cursor proof reads through: *sqlate.DB with
// every query's text kept, so the proof can say which spelling of the keyset
// predicate the engine ran.
type recorder struct {
	*sqlate.DB
	queries []string
}

func (r *recorder) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	r.queries = append(r.queries, q)
	return r.DB.QueryContext(ctx, q, args...)
}

// Proof: cursor paging end to end through the engine's row-value overlay. A
// walk by cursor over a two-column keyed prefix (text, then the uuid key)
// yields the rows of the offset read in the same order with no gap or
// repeat, the total holds on every page, the last page issues no cursor, the
// descending walk continues with the opposite comparison, and a cursor from
// another ordering is refused before any SQL.
func TestLive_CursorPagingThroughTheOverlay(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS live_page")
	if _, err := db.ExecContext(ctx, "CREATE TABLE live_page (id uuid PRIMARY KEY DEFAULT uuidv7(), grp text NOT NULL, n integer NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS live_page") })
	if _, err := db.ExecContext(ctx, "INSERT INTO live_page (grp, n) VALUES ('a', 1), ('a', 2), ('a', 3), ('b', 4), ('b', 5), ('c', 6), ('c', 7)"); err != nil {
		t.Fatal(err)
	}
	stmts := query.MustCatalog(postgres.Patterns()).MustCompile(pageFiles, "sql", db.Dialect())
	pages := stmts.Statement("pages").Project(query.Scanner[pageRow]())
	if err := query.Verify(ctx, db, stmts, pages); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	byGrp := []query.Sort{{Field: "grp"}}
	whole, err := pages.List(ctx, db, query.Directives{Sort: byGrp}, query.Page{Number: 1, Size: 10})
	if err != nil || len(whole.Items) != 7 || whole.Total != 7 || whole.More || whole.Next != "" {
		t.Fatalf("offset read = %+v, %v; want all 7 rows, no further page, no cursor", whole, err)
	}

	rec := &recorder{DB: db}
	var walked []pageRow
	d, size := query.Directives{Sort: byGrp}, 3
	c, err := pages.List(ctx, rec, d, query.Page{Number: 1, Size: size})
	for page := 1; ; page++ {
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if c.Total != 7 {
			t.Errorf("page %d: Total = %d, want 7 under a cursor too", page, c.Total)
		}
		walked = append(walked, c.Items...)
		if !c.More {
			if c.Next != "" {
				t.Errorf("the last page issued a cursor %q", c.Next)
			}
			break
		}
		if c.Next == "" {
			t.Fatalf("page %d reports a further page and no cursor", page)
		}
		if page == 3 {
			t.Fatal("the walk did not end after three pages")
		}
		c, err = pages.Continue(ctx, rec, d, c.Next, size)
	}
	if !slices.Equal(walked, whole.Items) {
		t.Errorf("cursor walk = %v\nwant the offset read %v", walked, whole.Items)
	}
	// Each continued page is one statement carrying both the row-value
	// keyset predicate and the window that counts its total.
	const rowValue = "(q.grp, q.id) > (CAST($1 AS text), CAST($2 AS uuid))"
	continued := 0
	for _, q := range rec.queries {
		if strings.Contains(q, rowValue) && strings.Contains(q, "COUNT(*) OVER ()") {
			continued++
		}
	}
	if continued != 2 {
		t.Errorf("%d queries carried the row-value keyset predicate and the window count, want 2 (pages 2 and 3):\n%s", continued, strings.Join(rec.queries, "\n"))
	}

	// Descending: the keyed prefix takes the sort's direction and the
	// comparison flips.
	desc := []query.Sort{{Field: "grp", Descending: true}}
	wholeDesc, err := pages.List(ctx, db, query.Directives{Sort: desc}, query.Page{Number: 1, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	rec.queries = nil
	first, err := pages.List(ctx, rec, query.Directives{Sort: desc}, query.Page{Number: 1, Size: 3})
	if err != nil || first.Next == "" {
		t.Fatalf("first descending page = %+v, %v", first, err)
	}
	second, err := pages.Continue(ctx, rec, query.Directives{Sort: desc}, first.Next, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := append(slices.Clone(first.Items), second.Items...); !slices.Equal(got, wholeDesc.Items[:6]) {
		t.Errorf("descending walk = %v\nwant %v", got, wholeDesc.Items[:6])
	}
	if last := rec.queries[len(rec.queries)-1]; !strings.Contains(last, "(q.grp, q.id) < (CAST($1 AS text), CAST($2 AS uuid))") {
		t.Errorf("descending continuation ran %q, want the row-value < comparison", last)
	}

	_, err = pages.Continue(ctx, db, query.Directives{Sort: byGrp}, first.Next, 3)
	var ce *query.CursorError
	if !errors.As(err, &ce) || ce.Reason != query.CursorMismatch || !errors.Is(err, query.ErrDirectives) {
		t.Errorf("cursor under another ordering = %v, want CursorMismatch", err)
	}
}

type versioned struct {
	row
	Version int64
}

func scanVersioned(rows query.Row) (versioned, error) {
	var v versioned
	err := rows.Scan(&v.ID, &v.Name, &v.N, &v.At, &v.Version)
	return v, err
}

// Proof: the typed guard's three outcomes against real rows, on both forms
// of the returning command: the engine's RETURNING, and the command then its
// read under a dialect that hides the capability. The publish command
// carries a predicate of its own beyond the key and the version (n > 1), so
// a row at the expected version can still refuse the write: no row is
// sql.ErrNoRows, another version is ErrVersionMismatch with both versions in
// its text, and the expected version refused by the command's own predicate
// is a *RefusedError carrying the row the handle read back. A hit returns
// the changed row at its new version.
func TestLive_RowGuardThreeWay(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	for _, form := range []struct {
		name    string
		dialect sqlate.Dialect
	}{
		{"native", postgres.Dialect{}},
		{"fallback", struct{ sqlate.Dialect }{postgres.Dialect{}}},
	} {
		t.Run(form.name, func(t *testing.T) {
			liveQ(t, db)
			stmts := query.MustCatalog(postgres.Patterns()).MustCompile(liveFiles, "sql", form.dialect)
			if native := stmts.Statement("publish").ReturningText() != ""; native != (form.name == "native") {
				t.Fatalf("ReturningText = %q under the %s form", stmts.Statement("publish").ReturningText(), form.name)
			}
			view := stmts.Statement("view").Project(scanRow)
			publish := stmts.Statement("publish").Returning(scanVersioned).Guarded("version", func(v versioned) int64 { return v.Version })
			a, err := view.One(ctx, db, "name", "a") // n = 1: the command's own predicate refuses it
			if err != nil {
				t.Fatal(err)
			}
			c, err := view.One(ctx, db, "name", "c") // n = 3
			if err != nil {
				t.Fatal(err)
			}

			if _, err := publish.Run(ctx, db, 1, query.Args{"id": "00000000-0000-0000-0000-000000000000", "name": "x"}); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("no row = %v, want sql.ErrNoRows", err)
			}
			_, err = publish.Run(ctx, db, 7, query.Args{"id": a.ID, "name": "x"})
			if !errors.Is(err, query.ErrVersionMismatch) || !strings.Contains(err.Error(), "expected 7, current 1") {
				t.Errorf("another version = %v, want ErrVersionMismatch expected 7, current 1", err)
			}
			_, err = publish.Run(ctx, db, 1, query.Args{"id": a.ID, "name": "x"})
			var refused *query.RefusedError[versioned]
			if !errors.As(err, &refused) || !errors.Is(err, query.ErrRefused) || refused.Version != 1 || refused.Row.ID != a.ID || refused.Row.Name != "a" || refused.Row.N != 1 || refused.Row.Version != 1 {
				t.Errorf("refused at the expected version = %v (%+v), want a RefusedError carrying row a at version 1", err, refused)
			}
			hit, err := publish.Run(ctx, db, 1, query.Args{"id": c.ID, "name": "C"})
			if err != nil || hit.ID != c.ID || hit.Name != "C" || hit.Version != 2 {
				t.Fatalf("hit = %+v, %v, want row c named C at version 2", hit, err)
			}
			if got, _ := view.One(ctx, db, "id", c.ID); got.Name != "C" {
				t.Errorf("the hit did not persist: %+v", got)
			}
			if got, _ := view.One(ctx, db, "id", a.ID); got.Name != "a" {
				t.Errorf("the refused write changed the row: %+v", got)
			}
		})
	}
}

var probeFiles = fstest.MapFS{
	"sql/misspelled.sql": {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid not null\n--| field: nam text\nSELECT id, name FROM live_q")},
	"sql/mistyped.sql":   {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid not null\n--| field: n text\nSELECT id, n FROM live_q")},
}

// Proof: the field-contract probe. A base whose SQL prepares but whose
// declared contract names a field it does not output, or declares a type
// the engine cannot compare its column against, fails Verify at startup:
// the misspelled field as an undefined column (42703), the mistyped one as
// an operator the engine does not have (42883).
func TestLive_TypeProbe(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	liveQ(t, db)
	probes := query.MustCatalog(postgres.Patterns()).MustCompile(probeFiles, "sql", db.Dialect())
	if err := probes.Verify(ctx, db); err != nil {
		t.Fatalf("the probe bases themselves do not prepare: %v", err)
	}
	none := func(query.Row) (struct{}, error) { return struct{}{}, nil }
	for _, tc := range []struct{ name, state string }{
		{"misspelled", "42703"},
		{"mistyped", "42883"},
	} {
		err := probes.Statement(tc.name).Project(none).Verify(ctx, db)
		if sqlState(err) != tc.state || !strings.Contains(err.Error(), "field contract") {
			t.Errorf("%s: Verify = %v, want SQLSTATE %s from the field-contract probe", tc.name, err, tc.state)
		} else {
			t.Logf("%s: %v", tc.name, err)
		}
	}
}

// Identity is the sub-struct the embedded-scan proof shares between
// entities. It is exported because the mapper reaches an embedded struct
// through an exported field only.
type Identity struct {
	ID        string
	CreatedAt sql.NullTime `db:"at"`
}

type entity struct {
	Identity
	Name string
	N    int64
}

// Proof: an untagged embedded struct flattens into the outer type's columns
// against a real multi-column row: id and at (through its db tag) land in
// the embedded Identity, name and n in the entity itself, both through the
// row handle and through the projection.
func TestLive_EmbeddedStructScan(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	liveQ(t, db)
	stmts := liveStatements(db)
	all, err := stmts.Statement("view").Scan(query.Scanner[entity]()).All(ctx, db, nil)
	if err != nil || len(all) != 3 {
		t.Fatalf("All = %v, %v; want 3 rows", all, err)
	}
	byName := map[string]entity{}
	for _, e := range all {
		byName[e.Name] = e
	}
	if a := byName["a"]; a.ID == "" || a.N != 1 || !a.CreatedAt.Valid || a.CreatedAt.Time.Year() != 2026 {
		t.Errorf("a = %+v, want an id, n 1, and a valid 2026 timestamp through the embedded Identity", a)
	}
	if b := byName["b"]; b.ID == "" || b.N != 2 || b.CreatedAt.Valid {
		t.Errorf("b = %+v, want an id, n 2, and a null timestamp", b)
	}
	c, err := stmts.Statement("view").Project(query.Scanner[entity]()).List(ctx, db, query.Directives{Sort: []query.Sort{{Field: "n"}}}, query.Page{Number: 1, Size: 10})
	if err != nil || len(c.Items) != 3 || c.Items[0].Name != "a" || c.Items[2].ID == "" {
		t.Errorf("projection over the scanner = %+v, %v", c, err)
	}
}

// Proof: the not-null violation's column. The engine names the column and
// the table on a null in a NOT NULL column and no constraint, so
// ConstraintError.Column is the consumer's handle to the field; a named
// constraint, the primary key here, fills Constraint instead. PostgreSQL 18
// records a NOT NULL as a named constraint in pg_constraint
// (live_q_name_not_null), and its error report still leaves the constraint
// name out.
func TestLive_NotNullNamesTheColumn(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	liveQ(t, db)
	stmts := liveStatements(db)
	_, err := stmts.Statement("insert").Exec(ctx, db, query.Args{"name": nil, "n": 1})
	var ce *sqlate.ConstraintError
	if !errors.As(err, &ce) || !errors.Is(err, sqlate.ErrNotNullViolation) || sqlState(err) != "23502" {
		t.Fatalf("null name = %v, want a ConstraintError of the not-null class over 23502", err)
	}
	if ce.Column != "name" || ce.Table != "live_q" {
		t.Errorf("ConstraintError column=%q table=%q, want name on live_q", ce.Column, ce.Table)
	}
	if ce.Constraint != "" {
		t.Errorf("ConstraintError constraint=%q, want empty: the engine's not-null error report names no constraint", ce.Constraint)
	}
	if !strings.Contains(err.Error(), `on column "name"`) {
		t.Errorf("error text = %q, want it to name the column", err)
	}
	t.Logf("not-null: %v", err)

	// The contrast: a named constraint fills Constraint.
	_, err = db.ExecContext(ctx, "INSERT INTO live_q (id, name, n) SELECT id, 'dup', 9 FROM live_q LIMIT 1")
	if !errors.As(err, &ce) || !errors.Is(err, sqlate.ErrUniqueViolation) || ce.Constraint != "live_q_pkey" {
		t.Errorf("duplicate key = %v, want a ConstraintError naming live_q_pkey", err)
	}
	t.Logf("unique: %v", err)
}
