package query_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

// returningFiles is a command that reads its row back through a qualified
// read, and an insert that reads it through an unqualified one.
var returningFiles = fstest.MapFS{
	"sql/rename.sql":    {Data: []byte("--| tier: standard\n--| returning: org_row\nUPDATE organization SET name = {{name}} WHERE id = {{id}}")},
	"sql/org_row.sql":   {Data: []byte("--| tier: standard\nSELECT o.id, o.name, o.version FROM organization o WHERE o.id = {{id}}")},
	"sql/create.sql":    {Data: []byte("--| tier: standard\n--| returning: plain_row\n/* a note */ insert into organization (id, name) VALUES ({{id}}, {{name}});")},
	"sql/plain_row.sql": {Data: []byte("--| tier: standard\nselect version, id\nfrom organization where id = {{id}}")},
}

func TestReturning_ResolvesTheReadAndItsColumns(t *testing.T) {
	stmts, err := catalog().Compile(returningFiles, "sql", sqltest.ReturningDialect{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	rename := stmts.Statement("rename")
	if rename.Reads() != "org_row" {
		t.Errorf("Reads() = %q", rename.Reads())
	}
	if want := "UPDATE organization SET name = $1 WHERE id = $2\nRETURNING id, name, version"; rename.ReturningText() != want {
		t.Errorf("ReturningText() = %q, want %q", rename.ReturningText(), want)
	}
	if rename.Text() != "UPDATE organization SET name = $1 WHERE id = $2" {
		t.Errorf("Text() = %q, want the command unchanged", rename.Text())
	}
	create := stmts.Statement("create")
	if want := "/* a note */ insert into organization (id, name) VALUES ($1, $2)\nRETURNING version, id"; create.ReturningText() != want {
		t.Errorf("ReturningText() = %q, want %q", create.ReturningText(), want)
	}
	if read := stmts.Statement("org_row"); read.Reads() != "" || read.ReturningText() != "" {
		t.Errorf("a read reports returning: %q, %q", read.Reads(), read.ReturningText())
	}
}

func TestReturning_HasNoSingleStatementFormWhenTheDialectLacksTheCapability(t *testing.T) {
	stmts, err := catalog().Compile(returningFiles, "sql", sqltest.Dialect{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	rename := stmts.Statement("rename")
	if rename.Reads() != "org_row" || rename.ReturningText() != "" {
		t.Errorf("Reads() = %q, ReturningText() = %q", rename.Reads(), rename.ReturningText())
	}
}

// decliner implements query.Returner and declines every verb.
type decliner struct{ sqltest.Dialect }

func (decliner) Returning(query.Verb, string, []string) (string, bool) { return "", false }

func TestReturning_HasNoSingleStatementFormWhenTheDialectDeclines(t *testing.T) {
	stmts, err := catalog().Compile(returningFiles, "sql", decliner{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := stmts.Statement("rename").ReturningText(); got != "" {
		t.Errorf("ReturningText() = %q", got)
	}
}

// recorder implements query.Returner, keeping what it was asked, and appends
// the clause.
type recorder struct {
	sqltest.Dialect
	verbs   *[]query.Verb
	bodies  *[]string
	columns *[][]string
}

func (r recorder) Returning(verb query.Verb, body string, columns []string) (string, bool) {
	*r.verbs = append(*r.verbs, verb)
	*r.bodies = append(*r.bodies, body)
	*r.columns = append(*r.columns, columns)
	return body + " RETURNING " + strings.Join(columns, ", "), true
}

func TestReturning_PassesTheDialectTheExpandedBodyVerbAndColumns(t *testing.T) {
	var verbs []query.Verb
	var bodies []string
	var columns [][]string
	fsys := fstest.MapFS{
		"sql/touch.sql": {Data: []byte("--| tier: standard\n--| returning: row\nUPDATE t SET {{> sql.guard_set}} WHERE id = {{id}} AND tags IN ({{tags...}})")},
		"sql/row.sql":   {Data: []byte("--| tier: standard\nSELECT t.b, t.a, t.c FROM t WHERE t.id = {{id}}")},
	}
	stmts, err := catalog().Compile(fsys, "sql", recorder{verbs: &verbs, bodies: &bodies, columns: &columns})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !slices.Equal(verbs, []query.Verb{query.Update}) || len(columns) != 1 || !slices.Equal(columns[0], []string{"b", "a", "c"}) {
		t.Fatalf("verbs %v columns %v", verbs, columns)
	}
	if strings.Contains(bodies[0], "{{>") || !strings.Contains(bodies[0], "{{tags...}}") {
		t.Errorf("body = %q, want includes expanded and parameters still slots", bodies[0])
	}
	touch := stmts.Statement("touch")
	if !strings.HasSuffix(touch.ReturningText(), " RETURNING b, a, c") || strings.Contains(touch.ReturningText(), "{{") {
		t.Errorf("ReturningText() = %q", touch.ReturningText())
	}
	if !strings.HasPrefix(touch.ReturningText(), touch.Text()) {
		t.Errorf("ReturningText() = %q does not render the command %q", touch.ReturningText(), touch.Text())
	}
}

// injector implements query.Returner and writes a parameter of its own.
type injector struct{ sqltest.Dialect }

func (injector) Returning(_ query.Verb, body string, _ []string) (string, bool) {
	return body + " RETURNING {{id}}", true
}

func TestReturning_RefusesADialectThatWritesAParameter(t *testing.T) {
	_, err := catalog().Compile(returningFiles, "sql", injector{})
	if err == nil || !strings.Contains(err.Error(), ".sql") || !strings.Contains(err.Error(), "introduces {{") {
		t.Errorf("Compile = %v, want the injected {{ refused", err)
	}
}

func TestReturning_RejectsBrokenDeclarations(t *testing.T) {
	const read = "--| tier: standard\nSELECT id, name FROM t WHERE id = {{id}}"
	const update = "UPDATE t SET name = {{name}} WHERE id = {{id}}"
	cases := map[string]struct {
		command, read, want string
	}{
		"not a name":            {"--| tier: standard\n--| returning: Row\n" + update, read, "is not a statement name"},
		"delete":                {"--| tier: standard\n--| returning: row\nDELETE FROM t WHERE id = {{id}}", read, `starts "DELETE"`},
		"leading with":          {"--| tier: standard\n--| returning: row\nWITH x AS (SELECT 1) UPDATE t SET name = {{name}} WHERE id = {{id}}", read, `starts "WITH"`},
		"insert without into":   {"--| tier: standard\n--| returning: row\nINSERT t VALUES ({{id}}, {{name}})", read, `starts "INSERT"`},
		"select":                {"--| tier: standard\n--| returning: row\nSELECT {{id}}, {{name}}", read, `starts "SELECT"`},
		"native command":        {"--| tier: native\n--| native: postgres\n--| returning: row\n" + update, read, "standard tier"},
		"command with a key":    {"--| tier: standard\n--| returning: row\n--| key: id\n--| field: id uuid\n" + update, read, "not a projection base"},
		"missing read":          {"--| tier: standard\n--| returning: ghost\n" + update, read, `"ghost" is not a statement`},
		"read returns":          {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\n--| returning: s\n" + update, "declares returning itself"},
		"read with a key":       {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\n--| key: id\n--| field: id uuid\n--| field: name text\nSELECT id, name FROM t", "declares a key or field"},
		"read with a field":     {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\n--| field: id uuid\nSELECT id, name FROM t", "declares a key or field"},
		"read in a transaction": {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\n--| transaction: required\n" + read[len("--| tier: standard\n"):], "requires a transaction"},
		"read's own parameter":  {"--| tier: standard\n--| returning: row\n" + update, read + " AND tenant = {{tenant}}", `parameter "tenant"`},
		"read not a select":     {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\nWITH x AS (SELECT 1) SELECT id FROM x", "is not SELECT"},
		"read selects a star":   {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\nSELECT * FROM t", `selects "*"`},
		"read selects an expr":  {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\nSELECT id, upper(name) FROM t", `selects "upper(name)"`},
		"read selects an alias": {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\nSELECT id, name AS n FROM t", `selects "name AS n"`},
		"read mixes qualifiers": {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\nSELECT a.id, b.name FROM a JOIN b ON a.id = b.id", "same qualifier"},
		"read half qualified":   {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\nSELECT t.id, name FROM t", "same qualifier"},
		"read repeats a column": {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\nSELECT a.id, a.id FROM a", `selects "id" twice`},
		"read without from":     {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\nSELECT 1", "is not SELECT"},
		"verb after a tab":      {"--| tier: standard\n--| returning: row\nMERGE\tINTO t USING u ON true", read, `starts "MERGE"`},
		"read upper case":       {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\nSELECT ID, name FROM t", `selects "ID"; each item is a lowercase column`},
		"read another table":    {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\nSELECT id, name FROM users WHERE id = {{id}}", `returning read "row" reads "users"; the command changes "t"`},
		"read another schema":   {"--| tier: standard\n--| returning: row\nUPDATE s.t SET name = {{name}} WHERE id = {{id}}", read, `reads "t"; the command changes "s.t"`},
		"read a subquery":       {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\nSELECT id, name FROM (SELECT id, name FROM t) q", "reads no table after FROM"},
		"read other qualifier":  {"--| tier: standard\n--| returning: row\n" + update, "--| tier: standard\nSELECT y.id, y.name FROM t x JOIN t y ON x.id = y.id", `qualifies its columns "y"; it reads "t" as "x"`},
		"insert reads another":  {"--| tier: standard\n--| returning: row\nINSERT INTO docs(id, name) VALUES ({{id}}, {{name}})", read, `reads "t"; the command changes "docs"`},
	}
	for name, c := range cases {
		fsys := fstest.MapFS{
			"sql/s.sql":   {Data: []byte(c.command)},
			"sql/row.sql": {Data: []byte(c.read)},
		}
		_, err := catalog().Compile(fsys, "sql", sqltest.ReturningDialect{})
		if err == nil || !strings.Contains(err.Error(), ".sql") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want a load error naming the file and %q", name, err, c.want)
		}
	}
}

func TestReturning_AcceptsAReadOfTheCommandsTable(t *testing.T) {
	cases := map[string]struct{ command, read string }{
		"bare table":              {"UPDATE t SET name = {{name}} WHERE id = {{id}}", "SELECT id, name FROM t WHERE id = {{id}}"},
		"alias":                   {"UPDATE t SET name = {{name}} WHERE id = {{id}}", "SELECT x.id, x.name FROM t x WHERE x.id = {{id}}"},
		"alias with as":           {"update t set name = {{name}} where id = {{id}}", "select x.id, x.name from t as x where x.id = {{id}}"},
		"qualified by the table":  {"UPDATE t SET name = {{name}} WHERE id = {{id}}", "SELECT t.id, t.name FROM t\nWHERE t.id = {{id}}"},
		"schema qualified":        {"UPDATE s.t SET name = {{name}} WHERE id = {{id}}", "SELECT t.id, t.name FROM s.t WHERE t.id = {{id}}"},
		"schema with an alias":    {"UPDATE s.t SET name = {{name}} WHERE id = {{id}}", "SELECT x.id, x.name FROM s.t AS x WHERE x.id = {{id}}"},
		"insert, no space":        {"INSERT INTO t(id, name) VALUES ({{id}}, {{name}})", "SELECT id, name FROM t WHERE id = {{id}}"},
		"a keyword, not an alias": {"UPDATE t SET name = {{name}} WHERE id = {{id}}", "SELECT id, name FROM t JOIN u ON u.id = t.id WHERE t.id = {{id}}"},
		"alone":                   {"UPDATE t SET name = {{name}} WHERE id = {{id}}", "SELECT id, name FROM t"},
	}
	for name, c := range cases {
		fsys := fstest.MapFS{
			"sql/s.sql":   {Data: []byte("--| tier: standard\n--| returning: row\n" + c.command)},
			"sql/row.sql": {Data: []byte("--| tier: standard\n" + c.read)},
		}
		stmts, err := catalog().Compile(fsys, "sql", sqltest.ReturningDialect{})
		if err != nil {
			t.Errorf("%s: Compile = %v", name, err)
			continue
		}
		if got := stmts.Statement("s").ReturningText(); !strings.HasSuffix(got, "RETURNING id, name") {
			t.Errorf("%s: ReturningText() = %q", name, got)
		}
	}
}

func TestReturning_RefusesAReturningCommandAsAProjectionBase(t *testing.T) {
	stmts := catalog().MustCompile(returningFiles, "sql", sqltest.Dialect{})
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "rename") {
			t.Errorf("recover() = %v, want a panic naming the command", r)
		}
	}()
	stmts.Statement("rename").Project[string](query.Scalar[string])
}

func TestVerify_PreparesTheSingleStatementFormAndNamesItsFailure(t *testing.T) {
	stmts := catalog().MustCompile(returningFiles, "sql", sqltest.ReturningDialect{})
	pool, rec := sqltest.Open(t)
	db := sqlate.Wrap(pool, sqltest.ReturningDialect{})
	if err := query.Verify(context.Background(), db, stmts); err != nil {
		t.Fatalf("Verify = %v", err)
	}
	prepared := rec.SQL(sqltest.OpPrepare)
	if len(prepared) != 6 || !slices.Contains(prepared, stmts.Statement("rename").ReturningText()) || !slices.Contains(prepared, stmts.Statement("rename").Text()) {
		t.Errorf("prepared %q, want the four statements and both single-statement forms", prepared)
	}
	boom := errors.New(`column "version" does not exist`)
	rec.FailPrepare = func(q string) error {
		if strings.Contains(q, "RETURNING id, name, version") {
			return boom
		}
		return nil
	}
	err := query.Verify(context.Background(), db, stmts)
	if err == nil || !strings.Contains(err.Error(), "query: rename (returning): ") || strings.Contains(err.Error(), "query: rename: ") {
		t.Errorf("Verify = %v, want the single-statement form's failure named", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("Verify = %v, want the prepare failure wrapped", err)
	}
}

// orgVersion is the row org_row reads: the organization and its version.
type orgVersion struct {
	ID      string
	Name    string
	Version int64
}

func scanOrgVersion(rows *sql.Rows) (orgVersion, error) {
	var o orgVersion
	err := rows.Scan(&o.ID, &o.Name, &o.Version)
	return o, err
}

var orgColumns = []string{"id", "name", "version"}

func orgVersionRows(n int) sqltest.Response {
	r := sqltest.Response{Columns: orgColumns}
	for i := range n {
		r.Rows = append(r.Rows, []driver.Value{string(rune('a' + i)), "New", int64(2)})
	}
	return r
}

// rename is the rename command's handle compiled for d.
func rename(t *testing.T, d sqlate.Dialect) query.Returning[orgVersion] {
	t.Helper()
	return catalog().MustCompile(returningFiles, "sql", d).Statement("rename").Returning(scanOrgVersion)
}

var renameArgs = query.Args{"id": "a", "name": "New"}

func TestReturningOne_Outcomes(t *testing.T) {
	type script struct {
		responses []sqltest.Response
		ops       []sqltest.Op
	}
	const (
		begin, exec, qry, commit, rollback = sqltest.OpBegin, sqltest.OpExec, sqltest.OpQuery, sqltest.OpCommit, sqltest.OpRollback
	)
	cases := []struct {
		name             string
		fallback, native *script // nil: the outcome does not arise on the form
		check            func(t *testing.T, row orgVersion, changed bool, err error)
	}{
		{
			name:     "changed",
			fallback: &script{[]sqltest.Response{{Affected: 1}, orgVersionRows(1)}, []sqltest.Op{begin, exec, qry, commit}},
			native:   &script{[]sqltest.Response{orgVersionRows(1)}, []sqltest.Op{qry}},
			check: func(t *testing.T, row orgVersion, changed bool, err error) {
				if err != nil || !changed || row != (orgVersion{"a", "New", 2}) {
					t.Errorf("One = %+v, %v, %v; want the changed row", row, changed, err)
				}
			},
		},
		{
			name:     "unchanged",
			fallback: &script{[]sqltest.Response{{Affected: 0}, orgVersionRows(1)}, []sqltest.Op{begin, exec, qry, commit}},
			native:   &script{[]sqltest.Response{orgVersionRows(0), orgVersionRows(1)}, []sqltest.Op{qry, qry}},
			check: func(t *testing.T, row orgVersion, changed bool, err error) {
				if err != nil || changed || row.ID != "a" {
					t.Errorf("One = %+v, %v, %v; want the row as it is, unchanged", row, changed, err)
				}
			},
		},
		{
			name:     "no row",
			fallback: &script{[]sqltest.Response{{Affected: 0}, orgVersionRows(0)}, []sqltest.Op{begin, exec, qry, rollback}},
			native:   &script{[]sqltest.Response{orgVersionRows(0), orgVersionRows(0)}, []sqltest.Op{qry, qry}},
			check: func(t *testing.T, _ orgVersion, changed bool, err error) {
				if err != sql.ErrNoRows || changed {
					t.Errorf("One = %v, %v; want bare sql.ErrNoRows", changed, err)
				}
			},
		},
		{
			name:     "more than one row",
			fallback: &script{[]sqltest.Response{{Affected: 2}}, []sqltest.Op{begin, exec, rollback}},
			native:   &script{[]sqltest.Response{orgVersionRows(2)}, []sqltest.Op{qry}},
			check: func(t *testing.T, _ orgVersion, _ bool, err error) {
				if !errors.Is(err, query.ErrNotOneRow) || !strings.Contains(err.Error(), "rename") {
					t.Errorf("err = %v, want ErrNotOneRow naming the command", err)
				}
			},
		},
		{
			name:     "command fails",
			fallback: &script{[]sqltest.Response{{Err: errDriver}}, []sqltest.Op{begin, exec, rollback}},
			native:   &script{[]sqltest.Response{{Err: errDriver}}, []sqltest.Op{qry}},
			check: func(t *testing.T, _ orgVersion, _ bool, err error) {
				if _, ok := errors.AsType[*sqltest.MappedError](err); !ok || !errors.Is(err, errDriver) {
					t.Errorf("err = %v, want the driver's error mapped", err)
				}
			},
		},
		{
			name:     "read vanished",
			fallback: &script{[]sqltest.Response{{Affected: 1}, orgVersionRows(0)}, []sqltest.Op{begin, exec, qry, rollback}},
			check: func(t *testing.T, _ orgVersion, _ bool, err error) {
				if !errors.Is(err, query.ErrNotOneRow) || !strings.Contains(err.Error(), "org_row") {
					t.Errorf("err = %v, want ErrNotOneRow naming the read", err)
				}
			},
		},
	}
	for _, f := range forms {
		for _, c := range cases {
			sc := c.fallback
			if f.native {
				sc = c.native
			}
			if sc == nil {
				continue
			}
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				pool, rec := sqltest.Open(t, sc.responses...)
				db := sqlate.Wrap(pool, f.dialect)
				row, changed, err := rename(t, f.dialect).One(context.Background(), db, renameArgs)
				c.check(t, row, changed, err)
				if !slices.Equal(rec.Ops(), sc.ops) || rec.Pending() != 0 || rec.RowsLeaked() != 0 {
					t.Errorf("ops %v, pending %d, leaked %d; want %v, 0, 0", rec.Ops(), rec.Pending(), rec.RowsLeaked(), sc.ops)
				}
			})
		}
	}
}

func TestReturningOne_RunsTheFormsText(t *testing.T) {
	for _, f := range forms {
		script := []sqltest.Response{{Affected: 1}, orgVersionRows(1)}
		want := []string{"UPDATE organization SET name = $1 WHERE id = $2", "SELECT o.id, o.name, o.version FROM organization o WHERE o.id = $1"}
		if f.native {
			script = script[1:]
			want = []string{"UPDATE organization SET name = $1 WHERE id = $2\nRETURNING id, name, version"}
		}
		pool, rec := sqltest.Open(t, script...)
		if _, _, err := rename(t, f.dialect).One(context.Background(), sqlate.Wrap(pool, f.dialect), renameArgs); err != nil {
			t.Fatalf("%s: One = %v", f.name, err)
		}
		var got []string
		for _, c := range rec.Calls() {
			if c.Op == sqltest.OpExec || c.Op == sqltest.OpQuery {
				got = append(got, c.SQL)
				if c.Args[0] != "New" && c.Args[0] != "a" {
					t.Errorf("%s: args %v", f.name, c.Args)
				}
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: ran %q, want %q", f.name, got, want)
		}
	}
}

func TestReturningOne_InsideATransactionOpensNone(t *testing.T) {
	ctx := context.Background()
	for _, f := range forms {
		script := []sqltest.Response{{Affected: 1}, orgVersionRows(1), {Affected: 2}}
		want := []sqltest.Op{sqltest.OpBegin, sqltest.OpExec, sqltest.OpQuery, sqltest.OpExec, sqltest.OpRollback}
		if f.native {
			script = []sqltest.Response{orgVersionRows(1), orgVersionRows(2)}
			want = []sqltest.Op{sqltest.OpBegin, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpRollback}
		}
		pool, rec := sqltest.Open(t, script...)
		db := sqlate.Wrap(pool, f.dialect)
		r := rename(t, f.dialect)
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, changed, err := r.One(ctx, tx, renameArgs); err != nil || !changed {
			t.Errorf("%s: One = %v, %v", f.name, changed, err)
		}
		if _, _, err := r.One(ctx, tx, renameArgs); !errors.Is(err, query.ErrNotOneRow) {
			t.Errorf("%s: err = %v, want ErrNotOneRow", f.name, err)
		}
		// The caller's transaction is the caller's to end.
		_ = tx.Rollback()
		if !slices.Equal(rec.Ops(), want) {
			t.Errorf("%s: ops %v, want %v", f.name, rec.Ops(), want)
		}
	}
}

// wrapped is a session that is neither a *sqlate.Tx nor a sqlate.Beginner.
type wrapped struct{ sqlate.Session }

// appDB is an application's own pool type, a sqlate.Beginner through the
// *sqlate.DB it embeds.
type appDB struct{ *sqlate.DB }

func TestReturningOne_TheFallbackNeedsATransactionOrABeginner(t *testing.T) {
	ctx := context.Background()
	pool, rec := sqltest.Open(t, sqltest.Response{Affected: 1}, orgVersionRows(1))
	db := sqlate.Wrap(pool, sqltest.Dialect{})
	r := rename(t, sqltest.Dialect{})
	if _, _, err := r.One(ctx, wrapped{db}, renameArgs); !errors.Is(err, query.ErrTransactionRequired) || len(rec.Calls()) != 0 {
		t.Errorf("wrapped: err = %v, ops %v; want ErrTransactionRequired before any SQL", err, rec.Ops())
	}
	if _, changed, err := r.One(ctx, appDB{db}, renameArgs); err != nil || !changed {
		t.Errorf("appDB: One = %v, %v", changed, err)
	}
	if want := []sqltest.Op{sqltest.OpBegin, sqltest.OpExec, sqltest.OpQuery, sqltest.OpCommit}; !slices.Equal(rec.Ops(), want) {
		t.Errorf("appDB: ops %v, want %v", rec.Ops(), want)
	}

	pool, rec = sqltest.Open(t, orgVersionRows(1))
	native := sqlate.Wrap(pool, sqltest.ReturningDialect{})
	if _, changed, err := rename(t, sqltest.ReturningDialect{}).One(ctx, wrapped{native}, renameArgs); err != nil || !changed || len(rec.Calls()) != 1 {
		t.Errorf("wrapped, single statement: One = %v, %v, ops %v; want one query", changed, err, rec.Ops())
	}
}

func TestReturningOne_OwnedTransactionFailures(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("connection lost")

	pool, rec := sqltest.Open(t, sqltest.Response{Err: errDriver})
	rec.FailRollback = boom
	_, _, err := rename(t, sqltest.Dialect{}).One(ctx, sqlate.Wrap(pool, sqltest.Dialect{}), renameArgs)
	if !errors.Is(err, errDriver) || !errors.Is(err, boom) || !strings.Contains(err.Error(), "rollback: ") {
		t.Errorf("err = %v, want the command's error with the rollback failure joined", err)
	}

	pool, rec = sqltest.Open(t)
	rec.FailBegin = boom
	if _, _, err := rename(t, sqltest.Dialect{}).One(ctx, sqlate.Wrap(pool, sqltest.Dialect{}), renameArgs); !errors.Is(err, sqlate.ErrConnectionFailed) {
		t.Errorf("begin failure = %v, want ErrConnectionFailed", err)
	}

	pool, rec = sqltest.Open(t, sqltest.Response{Affected: 1}, orgVersionRows(1))
	rec.FailCommit = boom
	if _, _, err := rename(t, sqltest.Dialect{}).One(ctx, sqlate.Wrap(pool, sqltest.Dialect{}), renameArgs); !errors.Is(err, boom) {
		t.Errorf("commit failure = %v, want it returned", err)
	}
}

func TestReturningOne_APanicRollsBackAndRepanics(t *testing.T) {
	pool, rec := sqltest.Open(t, sqltest.Response{Affected: 1}, orgVersionRows(1))
	stmts := catalog().MustCompile(returningFiles, "sql", sqltest.Dialect{})
	r := stmts.Statement("rename").Returning(func(*sql.Rows) (orgVersion, error) { panic("scan") })
	defer func() {
		if p := recover(); p != "scan" {
			t.Errorf("recover() = %v, want the scan's panic", p)
		}
		want := []sqltest.Op{sqltest.OpBegin, sqltest.OpExec, sqltest.OpQuery, sqltest.OpRollback}
		if !slices.Equal(rec.Ops(), want) {
			t.Errorf("ops %v, want %v", rec.Ops(), want)
		}
	}()
	_, _, _ = r.One(context.Background(), sqlate.Wrap(pool, sqltest.Dialect{}), renameArgs)
}

func TestReturningOne_TransactionRequiredRefusesBeforeAnySQL(t *testing.T) {
	fsys := fstest.MapFS{
		"sql/rename.sql":  {Data: []byte("--| tier: standard\n--| transaction: required\n--| returning: org_row\nUPDATE organization SET name = {{name}} WHERE id = {{id}}")},
		"sql/org_row.sql": returningFiles["sql/org_row.sql"],
	}
	ctx := context.Background()
	for _, f := range forms {
		pool, rec := sqltest.Open(t)
		db := sqlate.Wrap(pool, f.dialect)
		r := catalog().MustCompile(fsys, "sql", f.dialect).Statement("rename").Returning(scanOrgVersion)
		for _, s := range []sqlate.Session{db, appDB{db}, wrapped{db}} {
			if _, _, err := r.One(ctx, s, renameArgs); !errors.Is(err, query.ErrTransactionRequired) {
				t.Errorf("%s %T: err = %v, want ErrTransactionRequired", f.name, s, err)
			}
		}
		if len(rec.Calls()) != 0 {
			t.Errorf("%s: ops %v, want none", f.name, rec.Ops())
		}
	}
}

func TestReturningOne_BindsExpandedParametersOnBothForms(t *testing.T) {
	fsys := fstest.MapFS{
		"sql/tag.sql":    {Data: []byte("--| tier: standard\n--| returning: tagged\nUPDATE organization SET name = {{name}} WHERE id = {{id}} AND kind IN ({{kinds...}})")},
		"sql/tagged.sql": {Data: []byte("--| tier: standard\nSELECT id, name, version FROM organization WHERE id = {{id}} AND kind IN ({{kinds...}})")},
	}
	ctx := context.Background()
	for _, f := range forms {
		tag := catalog().MustCompile(fsys, "sql", f.dialect).Statement("tag")
		if f.native && !strings.HasSuffix(tag.ReturningText(), "AND kind IN ($3)\nRETURNING id, name, version") {
			t.Errorf("ReturningText() = %q, want the rendering at one element per list", tag.ReturningText())
		}
		r := tag.Returning(scanOrgVersion)
		for _, kinds := range [][]string{{"a", "b"}, {"a", "b", "c"}, {"a", "b"}} {
			script := []sqltest.Response{{Affected: 1}, orgVersionRows(1)}
			if f.native {
				script = script[1:]
			}
			pool, rec := sqltest.Open(t, script...)
			args := query.Args{"id": "x", "name": "New", "kinds": kinds}
			if _, changed, err := r.One(ctx, sqlate.Wrap(pool, f.dialect), args); err != nil || !changed {
				t.Fatalf("%s %v: One = %v, %v", f.name, kinds, changed, err)
			}
			for _, c := range rec.Calls() {
				switch c.Op {
				case sqltest.OpExec, sqltest.OpQuery:
					inList := "$" + strconv.Itoa(len(c.Args)) + ")"
					if !strings.Contains(c.SQL, inList) || c.Args[len(c.Args)-1] != kinds[len(kinds)-1] {
						t.Errorf("%s %v: %q bound %v", f.name, kinds, c.SQL, c.Args)
					}
				}
			}
			if f.native && !strings.HasSuffix(rec.Calls()[0].SQL, "\nRETURNING id, name, version") {
				t.Errorf("%s: ran %q, want the single-statement form", f.name, rec.Calls()[0].SQL)
			}
		}
	}
}

func TestReturning_PanicsOnAStatementWithoutTheDeclaration(t *testing.T) {
	stmts := catalog().MustCompile(returningFiles, "sql", sqltest.Dialect{})
	defer func() {
		r, _ := recover().(string)
		if !strings.Contains(r, "query: org_row: ") || !strings.Contains(r, "no returning") {
			t.Errorf("recover() = %q, want a panic naming the statement", r)
		}
	}()
	stmts.Statement("org_row").Returning(scanOrgVersion)
}
