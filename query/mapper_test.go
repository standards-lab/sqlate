package query_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

type entity struct {
	ID        string    `json:"id"`
	ParentID  *string   `json:"parent_id"`
	Name      string    `json:"name"`
	Nick      string    `json:"nick,omitempty" db:"nickname"`
	Derived   string    `json:"derived" db:"-"`
	CreatedAt time.Time `json:"created_at"`
	Plain     int64
	hidden    int //nolint:unused // proves unexported fields are skipped
}

// Identity is embedded by the types below. An embedded field takes the
// name of its type, so the type is exported to be indexed at all.
type Identity struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

// Audit is embedded by pointer, which contributes no column.
type Audit struct {
	Editor string `json:"editor"`
}

type embedder struct {
	Identity
	Name string `json:"name"`
}

type shadower struct {
	Identity
	ID string `json:"id"`
}

type carrier struct {
	Identity `db:"identity"`
	Name     string `json:"name"`
}

type auditable struct {
	*Audit
	Name string `json:"name"`
}

func TestScanner_MatchesColumnsToTagsInRowOrder(t *testing.T) {
	now := time.Now()
	db, rec := session(t, sqltest.Response{
		Columns: []string{"name", "id", "nickname", "parent_id", "created_at", "plain"},
		Rows:    [][]driver.Value{{"Acme", "a", "ac", nil, now, int64(7)}},
	})
	rows := source(t).Statement("all").Scan(query.Scanner[entity]())
	e, err := rows.One(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := entity{ID: "a", Name: "Acme", Nick: "ac", CreatedAt: now, Plain: 7}
	if !reflect.DeepEqual(e, want) {
		t.Errorf("scanned %+v, want %+v", e, want)
	}
	if rec.RowsLeaked() != 0 {
		t.Error("rows leaked")
	}
}

func TestScanner_UnknownColumnIsAnError(t *testing.T) {
	db, _ := session(t, sqltest.Response{Columns: []string{"id", "derived"}, Rows: [][]driver.Value{{"a", "x"}}})
	_, err := source(t).Statement("all").Scan(query.Scanner[entity]()).One(context.Background(), db, nil)
	if err == nil || !strings.Contains(err.Error(), `column "derived" has no field`) {
		t.Errorf("err = %v, want the unmapped column named", err)
	}
	if _, ok := errors.AsType[*sqltest.MappedError](err); !ok {
		t.Error("the scan failure did not cross the mapping boundary")
	}
}

// fakeRow is a Row that is not *sql.Rows: the shape of an adapter that
// shows a scan fewer columns than the statement returns.
type fakeRow struct {
	cols  []string
	vals  []any
	scans int
}

func (r *fakeRow) Columns() ([]string, error) { return r.cols, nil }

func (r *fakeRow) Scan(dest ...any) error {
	r.scans++
	if len(dest) != len(r.vals) {
		return fmt.Errorf("fakeRow: %d destinations for %d columns", len(dest), len(r.vals))
	}
	for i, d := range dest {
		reflect.ValueOf(d).Elem().Set(reflect.ValueOf(r.vals[i]))
	}
	return nil
}

func TestScanner_ReadsAnyRow(t *testing.T) {
	now := time.Now()
	row := &fakeRow{
		cols: []string{"plain", "id", "created_at"},
		vals: []any{int64(3), "a", now},
	}
	e, err := query.Scanner[entity]()(row)
	if err != nil {
		t.Fatal(err)
	}
	if want := (entity{ID: "a", CreatedAt: now, Plain: 3}); !reflect.DeepEqual(e, want) {
		t.Errorf("scanned %+v, want %+v", e, want)
	}
	if row.scans != 1 {
		t.Errorf("Scan called %d times, want once", row.scans)
	}
	_, err = query.Scanner[entity]()(&fakeRow{cols: []string{"id", "derived"}, vals: []any{"a", "x"}})
	if err == nil || !strings.Contains(err.Error(), `column "derived" has no field`) {
		t.Errorf("err = %v, want the unmapped column named", err)
	}
}

func TestScalar_ReadsAnyRow(t *testing.T) {
	n, err := query.Scalar[int64](&fakeRow{cols: []string{"count"}, vals: []any{int64(9)}})
	if err != nil || n != 9 {
		t.Errorf("Scalar = %d, %v; want 9", n, err)
	}
}

func TestArgsOf_BindsByColumnName(t *testing.T) {
	p := "p"
	args := query.ArgsOf(entity{ID: "a", ParentID: &p, Name: "n", Nick: "k", Derived: "d", Plain: 1})
	want := query.Args{"id": "a", "parent_id": &p, "name": "n", "nickname": "k", "created_at": time.Time{}, "plain": int64(1)}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
	if v, ok := query.ArgsOf(&entity{})["parent_id"]; !ok || v != nil {
		t.Errorf("nil pointer bound as %v, want NULL", v)
	}
	with := args.With("version", int64(3))
	if with["version"] != int64(3) || len(args) == len(with) {
		t.Error("With did not return an extended copy")
	}
	defer func() {
		if recover() == nil {
			t.Error("ArgsOf(non-struct) did not panic")
		}
	}()
	query.ArgsOf("x")
}

func TestEmbedded_FieldsAreColumnsInBothDirections(t *testing.T) {
	now := time.Now()
	db, rec := session(t, sqltest.Response{
		Columns: []string{"name", "id", "created_at"},
		Rows:    [][]driver.Value{{"Acme", "a", now}},
	})
	rows := source(t).Statement("all").Scan(query.Scanner[embedder]())
	e, err := rows.One(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := embedder{ID: "a", CreatedAt: now, Name: "Acme"}
	if !reflect.DeepEqual(e, want) {
		t.Errorf("scanned %+v, want %+v", e, want)
	}
	if rec.RowsLeaked() != 0 {
		t.Error("rows leaked")
	}
	args := query.ArgsOf(e)
	wantArgs := query.Args{"id": "a", "created_at": now, "name": "Acme"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

func TestEmbedded_OuterFieldShadowsTheEmbeddedOne(t *testing.T) {
	now := time.Now()
	db, _ := session(t, sqltest.Response{
		Columns: []string{"id", "created_at"},
		Rows:    [][]driver.Value{{"outer", now}},
	})
	rows := source(t).Statement("all").Scan(query.Scanner[shadower]())
	e, err := rows.One(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := shadower{CreatedAt: now, ID: "outer"}
	if !reflect.DeepEqual(e, want) {
		t.Errorf("scanned %+v, want %+v: the id column reached the embedded field", e, want)
	}
	args := query.ArgsOf(shadower{Identity: Identity{ID: "inner", CreatedAt: now}, ID: "outer"})
	wantArgs := query.Args{"id": "outer", "created_at": now}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
}

func TestEmbedded_TaggedStructIsOneColumn(t *testing.T) {
	args := query.ArgsOf(carrier{Name: "Acme"})
	want := query.Args{"identity": Identity{}, "name": "Acme"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
	db, _ := session(t, sqltest.Response{Columns: []string{"id"}, Rows: [][]driver.Value{{"a"}}})
	_, err := source(t).Statement("all").Scan(query.Scanner[carrier]()).One(context.Background(), db, nil)
	if err == nil || !strings.Contains(err.Error(), `column "id" has no field`) {
		t.Errorf("err = %v, want the tagged embed's inner column reported as unmapped", err)
	}
}

func TestEmbedded_PointerFieldIsSkipped(t *testing.T) {
	args := query.ArgsOf(auditable{Audit: &Audit{Editor: "e"}, Name: "Acme"})
	want := query.Args{"name": "Acme"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
	db, _ := session(t, sqltest.Response{Columns: []string{"editor"}, Rows: [][]driver.Value{{"e"}}})
	_, err := source(t).Statement("all").Scan(query.Scanner[auditable]()).One(context.Background(), db, nil)
	if err == nil || !strings.Contains(err.Error(), `column "editor" has no field`) {
		t.Errorf("err = %v, want the embedded pointer's column reported as unmapped", err)
	}
}
