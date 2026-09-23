package query

import (
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
)

// The struct-tag mapper: an entity's tags are its binding and scan
// contract, so a program writes neither scan functions nor Args literals.
// A field's column name is its `db` tag, else its `json` tag's name, else
// the field name lowercased; `db:"-"` excludes it. An untagged embedded
// struct flattens: its fields become columns of the outer type, as if
// declared there, and an outer field of the same name shadows one reached
// through an embed. A tagged embedded struct is one column instead,
// holding the whole embedded value; an embedded pointer contributes no
// column; the embedded type must itself be exported, since an unexported
// field is invisible to reflection. Column and parameter names coincide
// with the API vocabulary by convention, since a base aliases its output
// columns to the contract names, which is why the json tag is the usual
// source and db the override.

// Scanner returns the ScanFunc for T from its tags: each row's columns are
// matched to fields by name, in the row's order, and scanned into a fresh
// T. A column T has no field for is an error, so a SELECT list that grows
// past its entity fails loudly; a field with no column stays zero. It
// reads the row only through Row, matching the names Columns returns and
// filling them with one Scan, so it works over any Row, not only
// *sql.Rows.
func Scanner[T any]() ScanFunc[T] {
	fields := fieldsOf(reflect.TypeFor[T]())
	return func(row Row) (T, error) {
		var v T
		cols, err := row.Columns()
		if err != nil {
			return v, err
		}
		dests := make([]any, len(cols))
		rv := reflect.ValueOf(&v).Elem()
		for i, c := range cols {
			idx, ok := fields[c]
			if !ok {
				return v, fmt.Errorf("query: column %q has no field in %s", c, rv.Type())
			}
			dests[i] = rv.FieldByIndex(idx).Addr().Interface()
		}
		if err := row.Scan(dests...); err != nil {
			return v, err
		}
		return v, nil
	}
}

// ArgsOf binds a struct's fields as Args by their column names; a nil
// pointer binds NULL. It is the command-side twin of Scanner.
func ArgsOf(v any) Args {
	rv := reflect.Indirect(reflect.ValueOf(v))
	if rv.Kind() != reflect.Struct {
		panic(fmt.Sprintf("query: ArgsOf takes a struct, not %s", rv.Type()))
	}
	out := make(Args)
	for name, idx := range fieldsOf(rv.Type()) {
		f := rv.FieldByIndex(idx)
		if f.Kind() == reflect.Pointer && f.IsNil() {
			out[name] = nil
			continue
		}
		out[name] = f.Interface()
	}
	return out
}

// With binds one name: the first Args of a chain that continues with the
// With method, for a base's own parameters passed to List, Continue, and
// One.
func With(name string, v any) Args { return Args{name: v} }

// With returns a copy of a with name bound to v, for the inputs that arrive
// outside a command's body: the path id, the If-Match version.
func (a Args) With(name string, v any) Args {
	out := make(Args, len(a)+1)
	maps.Copy(out, a)
	out[name] = v
	return out
}

var fieldCache sync.Map // reflect.Type → map[string][]int

// fieldsOf indexes a struct type's exported fields by column name, once
// per type. Each name maps to the field's index path, which
// reflect.Value.FieldByIndex resolves: an untagged embedded struct is
// flattened, so the path reaches through it to the field it holds. A
// tagged embedded field is one column holding the whole embedded value,
// an embedded pointer contributes no column at all, and `db:"-"`
// excludes a field or an embedded struct entirely.
func fieldsOf(t reflect.Type) map[string][]int {
	if m, ok := fieldCache.Load(t); ok {
		return m.(map[string][]int)
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("query: %s is not a struct", t))
	}
	m := map[string][]int{}
	var embedded []int
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Tag.Get("db")
		if name == "" {
			name, _, _ = strings.Cut(f.Tag.Get("json"), ",")
		}
		tagged := name != ""
		if name == "-" {
			continue
		}
		if !tagged && f.Anonymous {
			switch f.Type.Kind() {
			case reflect.Struct:
				embedded = append(embedded, i)
				continue
			case reflect.Pointer:
				continue
			}
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		m[name] = []int{i}
	}
	// This type's own fields are indexed above and the embedded ones
	// below, so a name declared here shadows the same name reached
	// through an embedded struct; between two embedded structs offering
	// one name, the one declared first wins.
	for _, i := range embedded {
		for name, path := range fieldsOf(t.Field(i).Type) {
			if _, taken := m[name]; taken {
				continue
			}
			m[name] = append([]int{i}, path...)
		}
	}
	fieldCache.Store(t, m)
	return m
}
