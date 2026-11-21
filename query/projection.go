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
// rows per page. Both must be at least 1.
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
	// TotalExact counts the rows under the request's filters in the page's
	// own statement, as a window over the filtered base, so the total and
	// the page are read from one snapshot and cannot disagree.
	TotalExact TotalMode = iota
	// TotalNone skips the count; Collection.Total is NoTotal.
	TotalNone
)

// NoTotal is Collection.Total when the request declined the count, or when
// an empty page other than the first List page has no row to carry it.
const NoTotal = -1

const totalColumn = "sqlate_total"

// Collection is one page of a collection read: the items, the total under
// the request's filters, whether a further page exists, and the cursor that
// continues from this page's last item, empty when More is false or the
// ordering cannot be continued by cursor.
//
// Each row of a counted page carries the count, so a page with no row
// carries none. Total is the count when the page has an item, 0 for an
// empty first page of List, and NoTotal for an empty later page, an empty
// Continue page, or a request that declined the count. Whenever Total is
// not NoTotal on a List page, the page's items end at or before it, and
// More reports whether they end before it.
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
	Sort    []Sort
	Filters []Filter
	// Total says whether the read counts; the zero value does.
	Total TotalMode
}

// Projection is a base statement bound to a scan function and its declared
// field contract: the typed handle for a collection read. The collection
// pattern wraps the base as a derived table, so the base may be any query,
// a recursive CTE or a join tree, and the only names a declaration can
// reference are the base's output columns the header declared. The base's
// own parameters bind from the base arguments List, Continue, and One take,
// merged left to right with a later value winning.
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

func newProjection[T any](base Statement, scan ScanFunc[T]) Projection[T] {
	if base.returning != nil {
		panic(fmt.Sprintf("query: %s: a returning command is not a projection base", base.name))
	}
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
		if strings.EqualFold(f.Name, totalColumn) {
			panic(fmt.Sprintf("query: %s: field %q is the name the library reserves for the page's total", base.name, f.Name))
		}
		fields[f.Name] = f
	}
	return Projection[T]{base: base, scan: scan, fields: fields}
}

// Statement returns the base.
func (p Projection[T]) Statement() Statement { return p.base }

// List runs the collection read by offset, in one statement: the page under
// the declarations, the total under the same filters unless the request
// declined it, and the cursor that continues past the page. Sorts gain the
// key fields they do not already name, so the ordering is total and paging
// is stable; a page reads one row past its size to report whether a further
// page exists.
func (p Projection[T]) List(ctx context.Context, s sqlate.Session, d Directives, page Page, base ...Args) (Collection[T], error) {
	if page.Number < 1 {
		return Collection[T]{}, fmt.Errorf("%w: page number must be at least 1", ErrDirectives)
	}
	if page.Size < 1 {
		return Collection[T]{}, fmt.Errorf("%w: page size must be at least 1", ErrDirectives)
	}
	return p.list(ctx, s, d, page.Size, (page.Number-1)*page.Size, "", base)
}

// Continue runs the collection read from where a previous List or Continue
// left off: the size rows past after's position, under the same sort that
// issued it. after must be a Collection.Next a previous read returned; List
// reads a request's first page. The total and the next cursor are as List
// reports them.
func (p Projection[T]) Continue(ctx context.Context, s sqlate.Session, d Directives, after Cursor, size int, base ...Args) (Collection[T], error) {
	if after == "" {
		return Collection[T]{}, fmt.Errorf("%w: Continue requires a cursor from a previous page; use List for the first page", ErrDirectives)
	}
	if size < 1 {
		return Collection[T]{}, fmt.Errorf("%w: page size must be at least 1", ErrDirectives)
	}
	return p.list(ctx, s, d, size, 0, after, base)
}

func (p Projection[T]) list(ctx context.Context, s sqlate.Session, d Directives, size, offset int, after Cursor, base []Args) (Collection[T], error) {
	var none Collection[T]
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
	var afterValues []string
	if after != "" {
		if afterValues, err = p.decodeCursor(after, o, d.Filters); err != nil {
			return none, err
		}
	}

	counted := d.Total == TotalExact
	var keyset []string
	if after != "" {
		keyset = []string{p.keyset(b, o, afterValues)}
	}
	rows, err := s.QueryContext(ctx, p.page(b, predicates, keyset, o, offset, size+1, counted), b.values...)
	if err != nil {
		return none, p.engine(err)
	}
	defer func() { _ = rows.Close() }()

	var row Row = rows
	var adapter *countedRow
	if counted {
		cols, err := rows.Columns()
		if err != nil {
			return none, mapErr(s, err)
		}
		if adapter, err = newCountedRow(p.base.name, rows, cols); err != nil {
			return none, err
		}
		row = adapter
	}

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
	var out []T
	var last []string
	more := false
	for rows.Next() {
		if len(out) == size {
			more = true
			break
		}
		if index != nil && len(out) == size-1 {
			if last, err = p.readKeyed(rows, width, index, o); err != nil {
				return none, mapErr(s, err)
			}
		}
		if adapter != nil {
			adapter.read = false
		}
		v, err := p.scan(row)
		if err != nil {
			return none, mapErr(s, err)
		}
		if adapter != nil && !adapter.read {
			return none, fmt.Errorf("query: %s: the scan did not read its row, so the page's total is unread", p.base.name)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return none, mapErr(s, err)
	}
	total := NoTotal
	switch {
	case !counted:
	case len(out) > 0:
		total = int(adapter.total)
	case after == "" && offset == 0:
		total = 0
	}
	c := Collection[T]{Items: out, Total: total, More: more}
	if more && last != nil {
		if sig, ok := filterSignature(d.Filters); ok {
			c.Next = p.encodeCursor(o, sig, last)
		}
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

// Verify prepares three probes over the base. The first names every
// contract field and compares it against a cast of its declared type, so a
// field the base no longer outputs, or one whose type no longer matches,
// fails at startup. The second is one page past a cursor over the key
// alone, so the keyset predicate and the paging clause, an engine's own
// respelling of either included, are prepared at startup too. The third is
// the same page counted, so the window count's two layers are prepared at
// startup as well.
func (p Projection[T]) Verify(ctx context.Context, db sqlate.Session) error {
	cols := make([]string, 0, len(p.base.fields))
	predicates := make([]string, 0, len(p.base.fields))
	for _, f := range p.base.fields {
		cols = append(cols, "q."+f.Name)
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

	b := &binding{dialect: p.base.dialect, catalog: p.base.catalog, values: make([]any, len(p.base.compiled.params))}
	o, _ := p.order(nil)
	for _, probe := range []struct {
		name    string
		counted bool
	}{{"cursor page", false}, {"counted cursor page", true}} {
		pb := &binding{dialect: b.dialect, catalog: b.catalog, values: slices.Clone(b.values)}
		keyset := []string{p.keyset(pb, o, make([]string, len(o.keyed)))}
		stmt, err = db.PrepareContext(ctx, p.page(pb, nil, keyset, o, 0, 1, probe.counted))
		if err != nil {
			return fmt.Errorf("query: %s: %s: %w", p.base.name, probe.name, err)
		}
		if err := stmt.Close(); err != nil {
			return err
		}
	}
	return nil
}

type binding struct {
	dialect sqlate.Dialect
	catalog *Catalog
	values  []any
}

func (b *binding) value(field Field, v any) string {
	b.values = append(b.values, v)
	return b.catalog.render("value", map[string]string{"placeholder": b.dialect.Placeholder(len(b.values)), "type": field.Type})
}

func (b *binding) raw(v any) string {
	b.values = append(b.values, v)
	return b.dialect.Placeholder(len(b.values))
}

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

func (p Projection[T]) clause(predicates []string) string {
	if len(predicates) == 0 {
		return ""
	}
	return p.base.catalog.render("where", map[string]string{"predicates": strings.Join(predicates, " AND ")})
}

func (p Projection[T]) page(b *binding, predicates, keyset []string, o ordering, offset, fetch int, counted bool) string {
	paging := p.base.catalog.render("paging", map[string]string{"offset": b.raw(offset), "fetch": b.raw(fetch)})
	if counted {
		return p.base.catalog.render("collection_counted", map[string]string{"base": p.base.compiled.text, "where": p.clause(predicates), "after": p.clause(keyset), "order": o.text, "paging": paging})
	}
	return p.base.catalog.render("collection", map[string]string{"base": p.base.compiled.text, "where": p.clause(append(slices.Clip(predicates), keyset...)), "order": o.text, "paging": paging})
}

type countedRow struct {
	rows  *sql.Rows
	cols  []string
	total int64
	read  bool
}

func newCountedRow(base string, rows *sql.Rows, cols []string) (*countedRow, error) {
	if len(cols) == 0 || !strings.EqualFold(cols[len(cols)-1], totalColumn) {
		last := ""
		if len(cols) > 0 {
			last = cols[len(cols)-1]
		}
		return nil, fmt.Errorf("query: %s: a counted page's last column is %q, not %s", base, last, totalColumn)
	}
	visible := cols[:len(cols)-1]
	if slices.ContainsFunc(visible, func(c string) bool { return strings.EqualFold(c, totalColumn) }) {
		return nil, fmt.Errorf("query: %s: the base outputs a column named %s, which the library reserves", base, totalColumn)
	}
	return &countedRow{rows: rows, cols: visible}, nil
}

func (c *countedRow) Columns() ([]string, error) {
	return slices.Clone(c.cols), nil
}

func (c *countedRow) Scan(dest ...any) error {
	if len(dest) != len(c.cols) {
		return fmt.Errorf("sql: expected %d destination arguments in Scan, not %d", len(c.cols), len(dest))
	}
	all := make([]any, len(dest)+1)
	copy(all, dest)
	all[len(dest)] = &c.total
	c.read = true
	return c.rows.Scan(all...)
}

type term struct {
	field Field
	desc  bool
}

type ordering struct {
	terms      []term
	keyed      []term
	desc       bool
	cursorable bool
	text       string
}

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
		return v.UTC().Format(time.RFC3339Nano), nil
	default:
		return fmt.Sprint(v), nil
	}
}

func (p Projection[T]) engine(err error) error {
	if errors.Is(err, sqlate.ErrInvalidValue) {
		return &InvalidValueError{Err: err}
	}
	return err
}
