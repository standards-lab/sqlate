package query

import (
	"context"
	"database/sql"
	"reflect"
	"sync"

	"github.com/standards-lab/sqlate"
)

// Tier is a statement's declared portability.
type Tier string

const (
	// TierStandard is standard SQL: it runs on every engine the standard
	// covers with no port.
	TierStandard Tier = "standard"
	// TierNative uses one engine's reach; the native note names the reach
	// and the port.
	TierNative Tier = "native"
)

// Field is one entry of a projection base's field contract: the name a
// request may filter or sort by, and the SQL type a request value is cast
// to when it does, written as the engine reads it.
type Field struct {
	Name string
	Type string
}

// Args binds a statement's named parameters. A missing name is an
// ArgumentError; an extra name is ignored, so one map serves a guard's
// command and its narrower check.
type Args map[string]any

// Statement is one authored file, compiled: its text with the dialect's
// placeholders and its includes spliced, its parameters in position order,
// and its header. It carries the catalog it compiled against as it carries
// its dialect, so a projection over it composes from the same patterns. It
// is a value, fetched by name once at wiring and held in a typed handle.
type Statement struct {
	name       string
	compiled   compiled
	tier       Tier
	native     string
	txRequired bool
	key        string
	fields     []Field
	dialect    sqlate.Dialect
	catalog    *Catalog
	// renderings caches an expanded statement's text by the lengths of its
	// lists; nil for a statement with no expansion, whose text is final.
	renderings *sync.Map
}

// Catalog is the catalog the statement compiled against.
func (st Statement) Catalog() *Catalog { return st.catalog }

// Name is the file's base name without the .sql extension.
func (st Statement) Name() string { return st.name }

// Text is the SQL as the engine receives it: the file's body, parameters
// rewritten to the dialect's placeholders; the header is not sent. For a
// statement with an expanded parameter it is the rendering at one element
// per list, the text Verify prepares.
func (st Statement) Text() string { return st.compiled.text }

// Tier is the declared tier.
func (st Statement) Tier() Tier { return st.tier }

// Native is the native note: the reach used and the port, for a native
// statement; empty for a standard one.
func (st Statement) Native() string { return st.native }

// TransactionRequired reports the "-- transaction: required" header.
func (st Statement) TransactionRequired() bool { return st.txRequired }

// Params returns the parameter names in position order.
func (st Statement) Params() []string {
	out := make([]string, len(st.compiled.params))
	for i, p := range st.compiled.params {
		out[i] = p.name
	}
	return out
}

// Scan binds the statement to scan: the typed handle for a query that
// returns rows.
func (st Statement) Scan[T any](scan ScanFunc[T]) Rows[T] {
	return Rows[T]{stmt: st, scan: scan}
}

// Project binds the statement, a projection base, to scan: the typed
// handle for the collection read. A base without a key or field contract,
// or one that binds parameters of its own, is a wiring defect and panics.
func (st Statement) Project[T any](scan ScanFunc[T]) Projection[T] {
	return newProjection(st, scan)
}

// Guarded binds the statement, a guarded command, to its version check;
// version names the parameter both bind the expected version to.
func (st Statement) Guarded(check Statement, version string) Guard {
	return Guard{command: st, check: check, version: version}
}

// Key is the declared key of a projection base; empty otherwise.
func (st Statement) Key() string { return st.key }

// Fields returns the declared field contract, in header order.
func (st Statement) Fields() []Field {
	out := make([]Field, len(st.fields))
	copy(out, st.fields)
	return out
}

// Exec runs the statement and returns the rows affected.
func (st Statement) Exec(ctx context.Context, s sqlate.Session, args Args) (int64, error) {
	text, values, err := st.bind(s, args)
	if err != nil {
		return 0, err
	}
	res, err := s.ExecContext(ctx, text, values...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, mapErr(s, err)
}

// query runs the statement for its rows.
func (st Statement) query(ctx context.Context, s sqlate.Session, args Args) (*sql.Rows, error) {
	text, values, err := st.bind(s, args)
	if err != nil {
		return nil, err
	}
	return s.QueryContext(ctx, text, values...)
}

// bind checks the session against the transaction requirement and orders
// args by the statement's parameters, an expanded parameter's list
// flattened into consecutive values; for an expanded statement the text is
// rendered for the lists' lengths, cached by them.
func (st Statement) bind(s sqlate.Session, args Args) (string, []any, error) {
	if st.txRequired {
		if _, ok := s.(*sqlate.Tx); !ok {
			return "", nil, ErrTransactionRequired
		}
	}
	text := st.compiled.text
	var arity []int
	if st.compiled.template != nil {
		var key string
		var err error
		if arity, key, err = st.compiled.arities(st.name, args); err != nil {
			return "", nil, err
		}
		if cached, ok := st.renderings.Load(key); ok {
			text = cached.(string)
		} else {
			text = st.compiled.render(st.dialect.Placeholder, func(i int) int { return arity[i] })
			st.renderings.Store(key, text)
		}
	}
	values := make([]any, 0, len(st.compiled.params))
	for i, p := range st.compiled.params {
		v, ok := args[p.name]
		if !ok {
			return "", nil, &ArgumentError{Statement: st.name, Name: p.name}
		}
		if !p.expand {
			values = append(values, v)
			continue
		}
		rv := reflect.ValueOf(v)
		for k := range arity[i] {
			values = append(values, rv.Index(k).Interface())
		}
	}
	return text, values, nil
}

// mapErr routes an error that arose after a call returned — rows.Err, Scan,
// RowsAffected — through the session's mapper, the path the seam cannot
// cover itself.
func mapErr(s sqlate.Session, err error) error {
	if err == nil {
		return nil
	}
	if m, ok := s.(sqlate.ErrorMapper); ok {
		return m.MapError(err)
	}
	return err
}
