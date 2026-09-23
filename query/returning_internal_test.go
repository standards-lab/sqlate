package query

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// returningDialect is a stub dialect with the Returner capability; the
// package's own tests cannot import sqltest, which imports this package.
type returningDialect struct{}

func (returningDialect) Name() string             { return "test" }
func (returningDialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }
func (returningDialect) MapError(err error) error { return err }
func (returningDialect) Returning(_ Verb, body string, columns []string) (string, bool) {
	return body + " RETURNING " + strings.Join(columns, ", "), true
}

func TestReturning_RendersAnExpandedParameterInBothForms(t *testing.T) {
	fsys := fstest.MapFS{
		"sql/tag.sql": {Data: []byte("--| tier: standard\n--| returning: row\nUPDATE t SET tag = {{tag}} WHERE id = {{id}} AND kind IN ({{kinds...}})")},
		"sql/row.sql": {Data: []byte("--| tier: standard\nSELECT id, tag FROM t WHERE id = {{id}}")},
	}
	stmts, err := MustCatalog(Patterns()).Compile(fsys, "sql", returningDialect{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	st := stmts.Statement("tag")
	form := st.returning
	if form == nil || form.native == nil || form.renderings == nil || form.verb != Update || !slices.Equal(form.columns, []string{"id", "tag"}) || form.read.name != "row" {
		t.Fatalf("returning = %+v", form)
	}
	args := Args{"tag": "x", "id": 7, "kinds": []string{"a", "b", "c"}}
	text, values, err := st.bind(nil, args)
	if err != nil || text != "UPDATE t SET tag = $1 WHERE id = $2 AND kind IN ($3, $4, $5)" || len(values) != 5 {
		t.Errorf("command = %q %v %v", text, values, err)
	}
	for range 2 { // the second bind reads the form's own cache
		text, values, err = st.bindForm(*form.native, form.renderings, nil, args)
		if err != nil || text != "UPDATE t SET tag = $1 WHERE id = $2 AND kind IN ($3, $4, $5) RETURNING id, tag" || !slices.Equal(values, []any{"x", 7, "a", "b", "c"}) {
			t.Errorf("single statement = %q %v %v", text, values, err)
		}
	}
	if _, ok := form.renderings.Load("3,"); !ok {
		t.Error("the single-statement rendering was not cached in its own cache")
	}
	if st.ReturningText() != "UPDATE t SET tag = $1 WHERE id = $2 AND kind IN ($3) RETURNING id, tag" {
		t.Errorf("ReturningText() = %q, want the rendering at one element per list", st.ReturningText())
	}
}
