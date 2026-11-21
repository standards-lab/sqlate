package query

import (
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
)

// Scanner returns the ScanFunc for T from its tags: each row's columns are
// matched to fields by name, in the row's order, and scanned into a fresh
// T. A column T has no field for is an error, so a SELECT list that grows
// past its entity fails loudly; a field with no column stays zero. It
// reads the row only through Row, matching the names Columns returns and
// filling them with one Scan, so it works over any Row, not only
// *sql.Rows. A time field comes back in time.UTC, whatever time.Local is
// and whatever location the driver gave it: a time.Time, a *time.Time, and
// a valid sql.NullTime or sql.Null[time.Time], through embedded structs
// alike.
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
		for _, d := range dests {
			inUTC(d)
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

var fieldCache sync.Map

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
