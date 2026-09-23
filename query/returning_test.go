package query_test

import (
	"context"
	"errors"
	"slices"
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
