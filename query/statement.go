package query

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"sync"

	"github.com/standards-lab/sqlate"
)

// Tier is a statement's declared portability.
type Tier string

const (
	// TierStandard is standard SQL: it runs on every engine the standard
	// covers with no port.
	TierStandard Tier = "standard"
	// TierNative uses one engine's own feature; the native declaration names
	// the feature and the port.
	TierNative Tier = "native"
)

// Field is one entry of a projection base's field contract: the name a
// request may filter or sort by, and the SQL type a request value is cast
// to when it does, written as the engine reads it. NotNull reports the
// declaration's trailing "not null"; a field declared without it is
// presumed nullable.
type Field struct {
	Name    string
	Type    string
	NotNull bool
}

// Args binds a statement's named parameters. A missing name is an
// ArgumentError; an extra name is ignored, so one map serves a guard's
// command and its narrower check.
type Args map[string]any

// Statement is one authored file, compiled: its text with the dialect's
// placeholders and its includes spliced, its parameters in position order,
// and its header. It references the catalog it compiled against and its dialect,
// so a projection over it composes from the same patterns. It is a value,
// fetched by name once in a constructor and held in a typed handle.
type Statement struct {
	name       string
	compiled   compiled
	tier       Tier
	native     string
	port       string
	txRequired bool
	key        []string
	fields     []Field
	dialect    sqlate.Dialect
	catalog    *Catalog
	// renderings caches an expanded statement's text by the lengths of its
	// lists; nil for a statement with no expansion, whose text is final.
	renderings *sync.Map
	// returning is the resolved returning declaration of a command; nil for
	// a statement that declares none.
	returning *returnForm
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

// Reads is the name of the statement the returning declaration names, the
// read of the changed row; empty when none is declared.
func (st Statement) Reads() string {
	if st.returning == nil {
		return ""
	}
	return st.returning.read.name
}

// ReturningText is the single-statement form of a returning command as the
// engine receives it: the dialect's text, parameters rewritten to its
// placeholders, and for an expanded parameter the rendering at one element
// per list, the text Verify prepares. It is empty when no returning is
// declared or the dialect declined, where the command runs and then its
// read.
func (st Statement) ReturningText() string {
	if st.returning == nil || st.returning.native == nil {
		return ""
	}
	return st.returning.native.text
}

// Tier is the declared tier.
func (st Statement) Tier() Tier { return st.tier }

// Native is the native declaration: the engine feature used and the port,
// for a native statement; empty for a standard one.
func (st Statement) Native() string { return st.native }

// Port is the port declaration: how the statement is ported to another
// engine, for a native statement; empty when none is declared.
func (st Statement) Port() string { return st.port }

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
// a returning command, or one that takes an expanded parameter, is a defect
// in the caller's constructor and panics. A base's own non-expanded parameters bind from
// the base arguments List, Continue, and One take.
func (st Statement) Project[T any](scan ScanFunc[T]) Projection[T] {
	return newProjection(st, scan)
}

// Guarded binds the statement, a guarded command, to its version check;
// version names the parameter both bind the expected version to.
func (st Statement) Guarded(check Statement, version string) Guard {
	return Guard{command: st, check: check, version: version}
}

// GuardedRow binds the statement, a guarded command whose own predicate
// may refuse a row the key and version alone would have matched, to a
// row-returning check: version names the parameter both bind the expected
// version to, and current reads a checked row's own version.
func (st Statement) GuardedRow[T any](check Rows[T], version string, current func(T) int64) RowGuard[T] {
	return RowGuard[T]{command: st, check: check, version: version, current: current}
}

// Key is the declared key of a projection base, as the header declares it:
// a composite key's parts in header order, joined by ", ". It is empty when
// none is declared.
func (st Statement) Key() string {
	return strings.Join(st.key, ", ")
}

// Keys is the declared key of a projection base, its parts in header order;
// empty when none is declared.
func (st Statement) Keys() []string {
	out := make([]string, len(st.key))
	copy(out, st.key)
	return out
}

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
	return st.bindForm(st.compiled, st.renderings, s, args)
}

// bindForm is bind over one compiled form of the statement and that form's
// renderings cache: the statement's own, or a returning command's
// single-statement form, which binds the same parameters under the same
// transaction requirement.
func (st Statement) bindForm(c compiled, renderings *sync.Map, s sqlate.Session, args Args) (string, []any, error) {
	if st.txRequired {
		if _, ok := s.(*sqlate.Tx); !ok {
			return "", nil, ErrTransactionRequired
		}
	}
	text := c.text
	var arity []int
	if c.template != nil {
		var key string
		var err error
		if arity, key, err = c.arities(st.name, args); err != nil {
			return "", nil, err
		}
		if cached, ok := renderings.Load(key); ok {
			text = cached.(string)
		} else {
			text = c.render(st.dialect.Placeholder, func(i int) int { return arity[i] })
			renderings.Store(key, text)
		}
	}
	values := make([]any, 0, len(c.params))
	for i, p := range c.params {
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

// mapErr routes an error that arose after a call returned (rows.Err, Scan,
// RowsAffected) through the session's mapper, since the session's methods
// cannot see it.
func mapErr(s sqlate.Session, err error) error {
	if err == nil {
		return nil
	}
	if m, ok := s.(sqlate.ErrorMapper); ok {
		return m.MapError(err)
	}
	return err
}
