package query_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

var files = fstest.MapFS{
	"sql/organization_view.sql": {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid\n--| field: name text\n--| field: version integer\nSELECT id, name, version FROM organization")},
	"sql/lock_tree.sql":         {Data: []byte("--| tier: native\n--| native: postgres — pg_advisory_xact_lock. Ports: sp_getapplock.\n--| transaction: required\nSELECT pg_advisory_xact_lock({{key}})")},
	"sql/edit.sql":              {Data: []byte("--| tier: standard\nUPDATE organization SET name = {{name}} WHERE id = {{id}} AND version = {{version}}")},
	"sql/README.md":             {Data: []byte("not a statement")},
}

func TestLoad_ParsesTheHeaderIntoTheStatement(t *testing.T) {
	stmts, err := catalog().Compile(files, "sql", sqltest.Dialect{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	names := []string{}
	for _, st := range stmts.Statements() {
		names = append(names, st.Name())
	}
	if strings.Join(names, ",") != "edit,lock_tree,organization_view" {
		t.Errorf("inventory = %v", names)
	}

	view := stmts.Statement("organization_view")
	if view.Tier() != query.TierStandard || view.Key() != "id" || len(view.Fields()) != 3 ||
		view.Fields()[2] != (query.Field{Name: "version", Type: "integer"}) {
		t.Errorf("view = tier %s key %s fields %v", view.Tier(), view.Key(), view.Fields())
	}
	lock := stmts.Statement("lock_tree")
	if lock.Tier() != query.TierNative || !strings.HasPrefix(lock.Native(), "postgres") || !lock.TransactionRequired() {
		t.Errorf("lock = %+v", lock)
	}
	if edit := stmts.Statement("edit"); edit.TransactionRequired() || edit.Key() != "" || len(edit.Params()) != 3 {
		t.Errorf("edit = %+v", edit)
	}
	defer func() {
		if recover() == nil {
			t.Error("a missing statement did not panic")
		}
	}()
	stmts.Statement("missing")
}

func TestLoad_RejectsBrokenHeaders(t *testing.T) {
	cases := map[string]string{
		"no tier":                    "SELECT 1",
		"tier as prose":              "-- tier: standard\nSELECT 1",
		"bad tier":                   "--| tier: portable\nSELECT 1",
		"native without note":        "--| tier: native\nSELECT 1",
		"standard with note":         "--| tier: standard\n--| native: x\nSELECT 1",
		"unknown declaration":        "--| tier: standard\n--| teir: standard\nSELECT 1",
		"malformed declaration":      "--| tier: standard\n--| no colon here\nSELECT 1",
		"declaration after the body": "--| tier: standard\nSELECT 1\n--| key: id",
		"transaction none":           "--| tier: standard\n--| transaction: none\nSELECT 1",
		"field without kind":         "--| tier: standard\n--| field: id\nSELECT 1",
		"field with a bad type":      "--| tier: standard\n--| field: id uuid; drop\nSELECT 1",
		"key not a declared field":   "--| tier: standard\n--| key: id\n--| field: name text\nSELECT 1",
	}
	for name, text := range cases {
		_, err := catalog().Compile(fstest.MapFS{"sql/s.sql": {Data: []byte(text)}}, "sql", sqltest.Dialect{})
		if err == nil || !strings.Contains(err.Error(), "s.sql") {
			t.Errorf("%s: err = %v, want a load error naming the file", name, err)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("MustLoad did not panic")
		}
	}()
	catalog().MustCompile(fstest.MapFS{"sql/s.sql": {Data: []byte("SELECT 1")}}, "sql", sqltest.Dialect{})
}

func TestVerify_PreparesEveryStatementAndJoinsFailures(t *testing.T) {
	stmts, err := catalog().Compile(files, "sql", sqltest.Dialect{})
	if err != nil {
		t.Fatal(err)
	}
	pool, rec := sqltest.Open(t)
	db := sqlate.Wrap(pool, sqltest.Dialect{})
	boom := errors.New("column \"name\" does not exist")
	rec.FailPrepare = func(q string) error {
		if strings.Contains(q, "UPDATE organization") || strings.Contains(q, "pg_advisory") {
			return boom
		}
		return nil
	}
	err = query.Verify(context.Background(), db, stmts)
	if err == nil || !strings.Contains(err.Error(), "query: edit:") || !strings.Contains(err.Error(), "query: lock_tree:") {
		t.Fatalf("Verify = %v, want both failures named", err)
	}
	if _, ok := errors.AsType[*sqltest.MappedError](err); !ok {
		t.Error("the prepare failure did not cross the mapping boundary")
	}
	if got := rec.SQL(sqltest.OpPrepare); len(got) != 3 {
		t.Errorf("prepared %d statements, want 3", len(got))
	}
	rec.FailPrepare = nil
	if err := query.Verify(context.Background(), db, stmts); err != nil {
		t.Errorf("Verify on a satisfied schema = %v", err)
	}
}
