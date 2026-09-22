package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/standards-lab/sqlate"
)

// Page is the 1-based page declaration of a read: which page, and how many
// rows per page. Both must be at least 1. Number is ignored under a cursor,
// which continues from a position rather than an offset.
type Page struct {
	Number int
	Size   int
}

// Sort is one sort declaration, naming a contract field.
type Sort struct {
	Field      string
	Descending bool
}

// Op is a filter declaration's operator. The values are short strings so a
// read contract can state them verbatim.
type Op string

const (
	OpEq        Op = "eq"
	OpNe        Op = "ne"
	OpGt        Op = "gt"
	OpGe        Op = "ge"
	OpLt        Op = "lt"
	OpLe        Op = "le"
	OpLike      Op = "like"
	OpIsNull    Op = "null"
	OpIsNotNull Op = "notnull"
	OpIn        Op = "in"
)

// Filter is one filter declaration: a contract field, an operator, and the
// value. OpIsNull and OpIsNotNull ignore Value; OpIn requires a []any. A
// value binds as given, a request's text included, cast to the field's
// declared type, so the engine parses it and a value it cannot read is an
// InvalidValueError.
type Filter struct {
	Field string
	Op    Op
	Value any
}

// TotalMode says whether a collection read counts the rows under its
// filters. The zero value counts.
type TotalMode int

const (
	// TotalExact runs the count under the request's filters before the page.
	TotalExact TotalMode = iota
	// TotalNone skips the count; Collection.Total is NoTotal.
	TotalNone
)

// NoTotal is Collection.Total when the request declined the count.
const NoTotal = -1

// Collection is one page of a collection read: the items, the total under
// the request's filters (NoTotal when the request declined the count),
// whether a further page exists, and the cursor that continues from this
// page's last item, empty when More is false or the ordering cannot be
// continued by cursor.
type Collection[T any] struct {
	Items []T
	Total int
	More  bool
	Next  Cursor
}

// Directives is one read request against a projection. Filters and Sort
// reference contract field names; an unknown name is an UnknownFieldError,
// never SQL.
type Directives struct {
	Page    Page
	Sort    []Sort
	Filters []Filter
	// After continues from a cursor a previous page returned. Page.Number is
	// ignored under it; Page.Size is still required.
	After Cursor
	// Total says whether the read counts; the zero value does.
	Total TotalMode
}

// Projection is a base statement bound to a scan function and its declared
// field contract: the typed handle for a collection read. The collection
// pattern wraps the base as a derived table, so the base may be any query,
// a recursive CTE or a join tree, and the only names a declaration can
// reference are the base's output columns the header declared. The base's
// own parameters bind from the base arguments List and One take, merged
// left to right with a later value winning.
//
// Every part of the composed text is a pattern of the library's namespace
// in the base's catalog, as the library published it or an engine overlaid it;
// this code does only what cannot be text: the whitelist check against
// the header, list arity, and parameter positions.
type Projection[T any] struct {
	base   Statement
	scan   ScanFunc[T]
	fields map[string]Field
}

// newProjection is Statement.Project.
func newProjection[T any](base Statement, scan ScanFunc[T]) Projection[T] {
	if len(base.key) == 0 || len(base.fields) == 0 {
		panic(fmt.Sprintf("query: %s: a projection base declares a key and its fields", base.name))
	}
	for _, prm := range base.compiled.params {
		if prm.expand {
			panic(fmt.Sprintf("query: %s: a projection base takes no expanded parameter", base.name))
		}
	}
	fields := make(map[string]Field, len(base.fields))
	for _, f := range base.fields {
		fields[f.Name] = f
	}
	return Projection[T]{base: base, scan: scan, fields: fields}
}

// Statement returns the base.
func (p Projection[T]) Statement() Statement { return p.base }

// List runs the collection read: the page under the declarations, the total
// under the same filters unless the request declined it, and the cursor that
// continues past the page. Sorts gain the key fields they do not already
// name, so the ordering is total and paging is stable; a page reads one row
// past its size to report whether a further page exists.
func (p Projection[T]) List(ctx context.Context, s sqlate.Session, d Directives, base ...Args) (Collection[T], error) {
	var none Collection[T]
	if d.After == "" && d.Page.Number < 1 {
		return none, fmt.Errorf("%w: page number must be at least 1", ErrDirectives)
	}
	if d.Page.Size < 1 {
		return none, fmt.Errorf("%w: page size must be at least 1", ErrDirectives)
	}
	if d.Total != TotalExact && d.Total != TotalNone {
		return none, fmt.Errorf("%w: unknown total mode %d", ErrDirectives, d.Total)
	}
	b, err := p.bind(base)
	if err != nil {
		return none, err
	}
	predicates, err := p.predicates(b, d.Filters)
	if err != nil {
		return none, err
	}
	o, err := p.order(d.Sort)
	if err != nil {
		return none, err
	}
	var after []string
	if d.After != "" {
		if after, err = p.decodeCursor(d.After, o); err != nil {
			return none, err
		}
	}

	total := NoTotal
	if d.Total == TotalExact {
		rows, err := s.QueryContext(ctx, p.base.catalog.render("count", map[string]string{"base": p.base.compiled.text, "where": p.clause(predicates)}), b.values...)
		if err != nil {
			return none, p.engine(err)
		}
		if !rows.Next() {
			_ = rows.Close()
			return none, errors.New("query: count returned no row")
		}
		if err := rows.Scan(&total); err != nil {
			_ = rows.Close()
			return none, mapErr(s, err)
		}
		_ = rows.Close()
	}

	offset := 0
	if d.After == "" {
		offset = (d.Page.Number - 1) * d.Page.Size
	} else {
		predicates = append(predicates, p.keyset(b, o, after))
	}
	rows, err := s.QueryContext(ctx, p.page(b, predicates, o, offset, d.Page.Size+1), b.values...)
	if err != nil {
		return none, p.engine(err)
	}
	defer func() { _ = rows.Close() }()

	// The keyed columns are located once for the row set, and only when a
	// cursor can be issued at all.
	var index []int
	var width int
	if o.cursorable {
		cols, err := rows.Columns()
		if err != nil {
			return none, mapErr(s, err)
		}
		if index, err = p.keyedColumns(cols, o); err != nil {
			return none, err
		}
		width = len(cols)
	}
	out := make([]T, 0, d.Page.Size)
	var last []string
	more := false
	for rows.Next() {
		if len(out) == d.Page.Size {
			// The row past the page: it is never scanned, and its only
			// report is that a further page exists.
			more = true
			break
		}
		if index != nil && len(out) == d.Page.Size-1 {
			if last, err = p.readKeyed(rows, width, index, o); err != nil {
				return none, mapErr(s, err)
			}
		}
		v, err := p.scan(rows)
		if err != nil {
			return none, mapErr(s, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return none, mapErr(s, err)
	}
	c := Collection[T]{Items: out, Total: total, More: more}
	if more && last != nil {
		c.Next = p.encodeCursor(o, last)
	}
	return c, nil
}

// One runs the single-row read: the base under one equality filter on a
// contract field. No row is sql.ErrNoRows; when the field is not unique the
// first row wins.
func (p Projection[T]) One(ctx context.Context, s sqlate.Session, field string, value any, base ...Args) (T, error) {
	var zero T
	b, err := p.bind(base)
	if err != nil {
		return zero, err
	}
	predicates, err := p.predicates(b, []Filter{{Field: field, Op: OpEq, Value: value}})
	if err != nil {
		return zero, err
	}
	rows, err := s.QueryContext(ctx, p.base.catalog.render("one", map[string]string{"base": p.base.compiled.text, "where": p.clause(predicates)}), b.values...)
	if err != nil {
		return zero, p.engine(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return zero, mapErr(s, err)
		}
		return zero, sql.ErrNoRows
	}
	v, err := p.scan(rows)
	if err != nil {
		return zero, mapErr(s, err)
	}
	return v, mapErr(s, rows.Err())
}

// Verify prepares two probes over the base. The first names every contract
// field and compares it against a cast of its declared type, so a field the
// base no longer outputs, or one whose type no longer matches, fails at
// startup. The second is one page past a cursor over the key alone, so the
// keyset predicate and the paging clause, an engine's own respelling of
// either included, are prepared at startup too.
func (p Projection[T]) Verify(ctx context.Context, db sqlate.Session) error {
	cols := make([]string, 0, len(p.base.fields))
	predicates := make([]string, 0, len(p.base.fields))
	for _, f := range p.base.fields {
		cols = append(cols, "q."+f.Name)
		// The probe is only ever prepared, never executed, so the cast takes
		// the literal NULL in place of a placeholder and binds nothing.
		value := p.base.catalog.render("value", map[string]string{"placeholder": "NULL", "type": f.Type})
		predicates = append(predicates, p.base.catalog.render("filter_eq", map[string]string{"field": f.Name, "value": value}))
	}
	where := p.base.catalog.render("where", map[string]string{"predicates": strings.Join(predicates, " AND ")})
	stmt, err := db.PrepareContext(ctx, p.base.catalog.render("verify", map[string]string{"columns": strings.Join(cols, ", "), "base": p.base.compiled.text, "where": where}))
	if err != nil {
		return fmt.Errorf("query: %s: field contract: %w", p.base.name, err)
	}
	if err := stmt.Close(); err != nil {
		return err
	}

	// The base's own parameters keep the placeholders they compiled with, so
	// the binding starts with one unbound value each and the request-time
	// placeholders follow them; a prepare binds nothing.
	b := &binding{dialect: p.base.dialect, catalog: p.base.catalog, values: make([]any, len(p.base.compiled.params))}
	// The key alone, ascending: no sort names a field, so this cannot fail.
	o, _ := p.order(nil)
	text := p.page(b, []string{p.keyset(b, o, make([]string, len(o.keyed)))}, o, 0, 1)
	stmt, err = db.PrepareContext(ctx, text)
	if err != nil {
		return fmt.Errorf("query: %s: cursor page: %w", p.base.name, err)
	}
	return stmt.Close()
}

// binding is one composed read's bind list in placeholder order: the base's
// own values first, then each request value at the position its placeholder
// was allocated. Both queries of a List share one binding: the count runs
// over the values as they stand after the filters, and the page appends the
// cursor's values and the paging bounds after it.
type binding struct {
	dialect sqlate.Dialect
	catalog *Catalog
	values  []any
}

// value binds v at the next position and returns it as the value pattern's
// text, cast to field's declared type.
func (b *binding) value(field Field, v any) string {
	b.values = append(b.values, v)
	return b.catalog.render("value", map[string]string{"placeholder": b.dialect.Placeholder(len(b.values)), "type": field.Type})
}

// raw binds v at the next position and returns its bare placeholder, for
// the paging bounds.
func (b *binding) raw(v any) string {
	b.values = append(b.values, v)
	return b.dialect.Placeholder(len(b.values))
}

// bind seeds a binding with the base's own values in its parameter order,
// so every request-time placeholder is numbered after the base's. base is
// merged left to right, a later value winning; a parameter no Args names
// is an ArgumentError, and an extra name is ignored as Statement.bind
// ignores it.
func (p Projection[T]) bind(base []Args) (*binding, error) {
	b := &binding{dialect: p.base.dialect, catalog: p.base.catalog}
	if len(p.base.compiled.params) == 0 {
		return b, nil
	}
	merged := make(Args)
	for _, a := range base {
		maps.Copy(merged, a)
	}
	b.values = make([]any, 0, len(p.base.compiled.params))
	for _, prm := range p.base.compiled.params {
		v, ok := merged[prm.name]
		if !ok {
			return nil, &ArgumentError{Statement: p.base.name, Name: prm.name}
		}
		b.values = append(b.values, v)
	}
	return b, nil
}

// predicates lowers the filters to one predicate each, in filter order: the
// operator's filter pattern over the field, each request value bound through
// the value pattern's cast to the field's declared type.
func (p Projection[T]) predicates(b *binding, filters []Filter) ([]string, error) {
	if len(filters) == 0 {
		return nil, nil
	}
	predicates := make([]string, 0, len(filters))
	for _, f := range filters {
		field, ok := p.fields[f.Field]
		if !ok {
			return nil, &UnknownFieldError{Field: f.Field, Use: FieldUseFilter}
		}
		fill := map[string]string{"field": field.Name}
		switch f.Op {
		case OpEq, OpNe, OpGt, OpGe, OpLt, OpLe, OpLike:
			fill["value"] = b.value(field, f.Value)
		case OpIsNull, OpIsNotNull:
		case OpIn:
			vals, ok := f.Value.([]any)
			if !ok || len(vals) == 0 {
				return nil, &InvalidValueError{Field: f.Field, Err: errors.New("an in filter takes a non-empty []any")}
			}
			values := make([]string, len(vals))
			for i, v := range vals {
				values[i] = b.value(field, v)
			}
			fill["values"] = strings.Join(values, ", ")
		default:
			return nil, &UnknownOperatorError{Op: f.Op}
		}
		predicates = append(predicates, p.base.catalog.render("filter_"+string(f.Op), fill))
	}
	return predicates, nil
}

// clause renders the WHERE clause over predicates; empty when there are none.
func (p Projection[T]) clause(predicates []string) string {
	if len(predicates) == 0 {
		return ""
	}
	return p.base.catalog.render("where", map[string]string{"predicates": strings.Join(predicates, " AND ")})
}

// page composes the page text: the base as a derived table under predicates
// and o, with offset and fetch bound last.
func (p Projection[T]) page(b *binding, predicates []string, o ordering, offset, fetch int) string {
	paging := p.base.catalog.render("paging", map[string]string{"offset": b.raw(offset), "fetch": b.raw(fetch)})
	return p.base.catalog.render("collection", map[string]string{"base": p.base.compiled.text, "where": p.clause(predicates), "order": o.text, "paging": paging})
}

// term is one ORDER BY term.
type term struct {
	field Field
	desc  bool
}

// ordering is a request's resolved sort: every ORDER BY term after the key
// fields were appended, the keyed prefix a cursor continues from, its shared
// direction, whether a cursor can continue it, and the rendered clause.
type ordering struct {
	// terms is the caller's sorts, then each key field absent from them, in
	// key order.
	terms []term
	// keyed is the shortest prefix of terms that contains every key field:
	// the terms a cursor records, since they order the rows uniquely.
	keyed []term
	// desc is the keyed prefix's shared direction; false when it has none.
	desc bool
	// cursorable reports one direction across the keyed prefix and every
	// field in it declared not null, the two conditions the keyset predicate
	// needs to select exactly the rows past a cursor's row.
	cursorable bool
	// text is the ORDER BY clause.
	text string
}

// order resolves the sorts against the field contract and appends the key
// fields the caller did not sort by, so the ordering is total. An appended
// key field takes the keyed prefix's direction, since a cursor compares the
// whole prefix one way.
func (p Projection[T]) order(sorts []Sort) (ordering, error) {
	var o ordering
	o.terms = make([]term, 0, len(sorts)+len(p.base.key))
	for _, s := range sorts {
		f, ok := p.fields[s.Field]
		if !ok {
			return o, &UnknownFieldError{Field: s.Field, Use: FieldUseSort}
		}
		o.terms = append(o.terms, term{field: f, desc: s.Descending})
	}
	// The keyed prefix reaches the last key field the caller sorted by; a key
	// field the caller left out is appended, so the prefix is every term.
	covered, last := 0, -1
	for _, k := range p.base.key {
		for i, t := range o.terms {
			if t.field.Name == k {
				covered++
				last = max(last, i)
				break
			}
		}
	}
	keyed := last + 1
	if covered < len(p.base.key) {
		keyed = len(o.terms) + len(p.base.key) - covered
	}
	uniform := true
	for i := 1; i < min(keyed, len(o.terms)); i++ {
		uniform = uniform && o.terms[i].desc == o.terms[0].desc
	}
	if uniform && len(o.terms) > 0 {
		o.desc = o.terms[0].desc
	}
	for _, k := range p.base.key {
		if !slices.ContainsFunc(o.terms, func(t term) bool { return t.field.Name == k }) {
			o.terms = append(o.terms, term{field: p.fields[k], desc: o.desc})
		}
	}
	o.keyed = o.terms[:keyed]
	o.cursorable = uniform
	for _, t := range o.keyed {
		o.cursorable = o.cursorable && t.field.NotNull
	}
	rendered := make([]string, len(o.terms))
	for i, t := range o.terms {
		name := "order_term"
		if t.desc {
			name = "order_term_desc"
		}
		rendered[i] = p.base.catalog.render(name, map[string]string{"field": t.field.Name})
	}
	o.text = p.base.catalog.render("order", map[string]string{"terms": strings.Join(rendered, ", ")})
	return o, nil
}

// keyset composes the keyset predicate for continuing past values under o:
// each keyed value bound once, cast to its field's type, then the expanded
// chain of disjuncts for the standard spelling and the column, operator, and
// value lists for an overlay's row-value spelling; render takes whichever
// slots the catalog's pattern declares.
func (p Projection[T]) keyset(b *binding, o ordering, values []string) string {
	bound := make([]string, len(o.keyed))
	columns := make([]string, len(o.keyed))
	for i, t := range o.keyed {
		bound[i] = b.value(t.field, values[i])
		columns[i] = "q." + t.field.Name
	}
	cmp, op := "filter_gt", ">"
	if o.desc {
		cmp, op = "filter_lt", "<"
	}
	disjuncts := make([]string, len(o.keyed))
	for i, t := range o.keyed {
		conj := make([]string, 0, i+1)
		for j := range i {
			conj = append(conj, p.base.catalog.render("filter_eq", map[string]string{"field": o.keyed[j].field.Name, "value": bound[j]}))
		}
		conj = append(conj, p.base.catalog.render(cmp, map[string]string{"field": t.field.Name, "value": bound[i]}))
		if len(conj) == 1 {
			disjuncts[i] = conj[0]
		} else {
			disjuncts[i] = "(" + strings.Join(conj, " AND ") + ")"
		}
	}
	return p.base.catalog.render("keyset", map[string]string{
		"disjuncts": strings.Join(disjuncts, " OR "),
		"columns":   strings.Join(columns, ", "),
		"op":        op,
		"values":    strings.Join(bound, ", "),
	})
}

// keyedColumns locates each keyed term among the page's columns: the base's
// output column of the same name, matched exactly, else case-insensitively,
// since an engine folds an unquoted alias's case. A keyed field the page does
// not output is the contract's defect, which Verify reports at startup.
func (p Projection[T]) keyedColumns(cols []string, o ordering) ([]int, error) {
	index := make([]int, len(o.keyed))
	for k, t := range o.keyed {
		i := slices.Index(cols, t.field.Name)
		if i < 0 {
			i = slices.IndexFunc(cols, func(c string) bool { return strings.EqualFold(c, t.field.Name) })
		}
		if i < 0 {
			return nil, fmt.Errorf("query: %s: keyed field %q is not an output column of the base", p.base.name, t.field.Name)
		}
		index[k] = i
	}
	return index, nil
}

// readKeyed scans the current row a second time, into untyped destinations,
// and renders the keyed values as text; it runs before the consumer's scan
// because a scan into sql.RawBytes forbids a later Scan of the same row.
func (p Projection[T]) readKeyed(rows *sql.Rows, width int, index []int, o ordering) ([]string, error) {
	dests := make([]any, width)
	for i := range dests {
		dests[i] = new(any)
	}
	if err := rows.Scan(dests...); err != nil {
		return nil, err
	}
	out := make([]string, len(index))
	for k, i := range index {
		s, err := cursorValue(*dests[i].(*any))
		if err != nil {
			return nil, fmt.Errorf("query: %s: keyed field %q: %w", p.base.name, o.keyed[k].field.Name, err)
		}
		out[k] = s
	}
	return out, nil
}

// cursorValue renders a driver value as the text the field's CAST reads
// back: the driver.Value kinds database/sql delivers, a text column a driver
// returns as bytes included. A null is a contract violation, since a keyed
// field is declared not null.
func cursorValue(v any) (string, error) {
	switch v := v.(type) {
	case nil:
		return "", errors.New("is null; a keyed field is declared not null")
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	case time.Time:
		return v.Format(time.RFC3339Nano), nil
	default:
		return fmt.Sprint(v), nil
	}
}

// engine classifies a query failure: a data exception (a value the engine
// could not read as the field's type) is the request's fault and becomes
// an InvalidValueError; anything else is returned mapped.
func (p Projection[T]) engine(err error) error {
	if errors.Is(err, sqlate.ErrInvalidValue) {
		return &InvalidValueError{Err: err}
	}
	return err
}
