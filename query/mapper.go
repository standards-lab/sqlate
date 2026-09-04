package query

import (
	"database/sql"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
)

// The struct-tag mapper: an entity's tags are its binding and scan
// contract, so a domain writes neither scan functions nor Args literals.
// A field's column name is its `db` tag, else its `json` tag's name, else
// the field name lowercased; `db:"-"` excludes it. Column and parameter
// names coincide with the API vocabulary by the architecture's own rule —
// the base aliases its output columns to the contract names — which is why
// the json tag is the usual source and db the override.

// Scanner returns the ScanFunc for T from its tags: each row's columns are
// matched to fields by name, in the row's order, and scanned into a fresh
// T. A column T has no field for is an error, so a SELECT list that grows
// past its entity fails loudly; a field with no column stays zero.
func Scanner[T any]() ScanFunc[T] {
	fields := fieldsOf(reflect.TypeFor[T]())
	return func(rows *sql.Rows) (T, error) {
		var v T
		cols, err := rows.Columns()
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
			dests[i] = rv.Field(idx).Addr().Interface()
		}
		if err := rows.Scan(dests...); err != nil {
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
		f := rv.Field(idx)
		if f.Kind() == reflect.Pointer && f.IsNil() {
			out[name] = nil
			continue
		}
		out[name] = f.Interface()
	}
	return out
}

// With returns a copy of a with name bound to v, for the inputs that arrive
// outside a command's body — the path id, the If-Match version.
func (a Args) With(name string, v any) Args {
	out := make(Args, len(a)+1)
	maps.Copy(out, a)
	out[name] = v
	return out
}

var fieldCache sync.Map // reflect.Type → map[string]int

// fieldsOf indexes a struct type's exported fields by column name, once
// per type.
func fieldsOf(t reflect.Type) map[string]int {
	if m, ok := fieldCache.Load(t); ok {
		return m.(map[string]int)
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("query: %s is not a struct", t))
	}
	m := map[string]int{}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Tag.Get("db")
		if name == "" {
			name, _, _ = strings.Cut(f.Tag.Get("json"), ",")
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		if name == "-" {
			continue
		}
		m[name] = i
	}
	fieldCache.Store(t, m)
	return m
}
