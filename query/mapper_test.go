package query_test

import (
	"context"
	"database/sql/driver"
	"errors"
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
