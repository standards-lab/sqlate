package query

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/standards-lab/sqlate"
)

// Verb is the kind of command a returning clause attaches to.
type Verb string

const (
	// Insert is a command whose body starts INSERT INTO.
	Insert Verb = "INSERT"
	// Update is a command whose body starts UPDATE.
	Update Verb = "UPDATE"
)

// Returner is the dialect capability that renders a returning command's
// single-statement form, the command that returns its changed row. body is
// the command after include expansion, its parameters still {{…}} slots;
// columns are the read's bare column names in scan order. Returning with
// ok=false declines, and the command runs the fallback: the command and
// then its read. An implementation declines a Verb it does not know.
type Returner interface {
	Returning(verb Verb, body string, columns []string) (text string, ok bool)
}

// returnForm is a command's resolved returning declaration. read reads the
// changed row back and is also the fallback's second step; verb is the
// command's verb; columns are the read's bare column names in scan order;
// native is the single-statement form the dialect rendered, nil when the
// dialect declined or does not implement Returner. renderings caches the
// single-statement form's expanded text by the lists' lengths, as
// Statement.renderings does the command's own; it is nil when the form has
// no expansion or there is no form.
type returnForm struct {
	read       Statement
	verb       Verb
	columns    []string
	native     *compiled
	renderings *sync.Map
}

var (
	// leadingComment is a line or block comment at the start of a body,
	// skipped when reading the statement's leading keyword.
	leadingComment = regexp.MustCompile(`^(?:\s+|--[^\n]*(?:\n|$)|/\*(?s:.*?)\*/)+`)
	// commandVerb is the leading keyword a returning command starts with,
	// and the table it changes: the name after INSERT INTO or UPDATE, schema
	// qualified or not, as written.
	commandVerb = regexp.MustCompile(`(?i)^(insert\s+into|update)\s+([^\s(,;]*)`)
	// readList is the SELECT list of a returning read: everything before its
	// first FROM, each item then checked on its own.
	readList = regexp.MustCompile(`(?is)^select\s+(.+?)\s+from\s`)
	// readFrom is what follows a returning read's first FROM: the table it
	// reads, schema qualified or not, and the word after it, the table's
	// alias unless it is a keyword, with or without AS.
	readFrom = regexp.MustCompile(`(?i)^\s*([^\s(),;]+)(?:\s+(?:as\s+)?([a-z_][a-z0-9_]*))?`)
	// readColumn is one item of a returning read's SELECT list: a column,
	// optionally qualified by a table or alias.
	readColumn = regexp.MustCompile(`^(?:([a-z_][a-z0-9_]*)\.)?([a-z_][a-z0-9_]*)$`)
)

// stripLeading returns body without its leading whitespace and comments.
func stripLeading(body string) string {
	return body[len(leadingComment.FindString(body)):]
}

// commandVerbOf reads the verb of a returning command's body, INSERT INTO or
// UPDATE, matched case-insensitively, and the table it changes, as written.
// Anything else, DELETE or a leading WITH among them, is an error naming what
// the body starts with.
func commandVerbOf(body string) (Verb, string, error) {
	rest := stripLeading(body)
	m := commandVerb.FindStringSubmatch(rest)
	if m == nil {
		first := ""
		if f := strings.Fields(rest); len(f) > 0 {
			first = f[0]
		}
		return "", "", fmt.Errorf("a returning command is an INSERT INTO or an UPDATE; this one starts %q", first)
	}
	if m[2] == "" {
		return "", "", fmt.Errorf("a returning command names the table it changes after %s", strings.ToUpper(strings.Join(strings.Fields(m[1]), " ")))
	}
	if strings.EqualFold(m[1], "update") {
		return Update, m[2], nil
	}
	return Insert, m[2], nil
}

// fromKeywords are the words that may follow a read's FROM table and are
// not its alias.
var fromKeywords = []string{
	"where", "join", "inner", "left", "right", "full", "cross", "natural",
	"group", "order", "having", "limit", "offset", "fetch", "for", "window",
	"union", "intersect", "except", "on", "using",
}

// readShape is what a returning read's body declares: the returned columns
// in scan order, the qualifier every item carries (empty for none), and
// the table after its first FROM with its alias (empty for none).
type readShape struct {
	columns   []string
	qualifier string
	table     string
	alias     string
}

// readShapeOf reads the shape of a returning read's body: the body is
// SELECT <c>, <c>, … FROM <table> …, each <c> a lowercase column or
// table.column, every item under the same qualifier or none, no name twice.
// The columns are the bare names, in the list's order.
func readShapeOf(body string) (readShape, error) {
	var shape readShape
	text := stripLeading(body)
	m := readList.FindStringSubmatchIndex(text)
	if m == nil {
		return shape, errors.New("is not SELECT <column>, … FROM …")
	}
	for i, item := range strings.Split(text[m[2]:m[3]], ",") {
		item = strings.TrimSpace(item)
		c := readColumn.FindStringSubmatch(item)
		if c == nil {
			if readColumn.MatchString(strings.ToLower(item)) {
				return shape, fmt.Errorf("selects %q; each item is a lowercase column or table.column", item)
			}
			return shape, fmt.Errorf("selects %q; each item is a column or table.column", item)
		}
		if i == 0 {
			shape.qualifier = c[1]
		} else if c[1] != shape.qualifier {
			return shape, fmt.Errorf("selects %q; every item has the same qualifier or none", item)
		}
		if slices.Contains(shape.columns, c[2]) {
			return shape, fmt.Errorf("selects %q twice", c[2])
		}
		shape.columns = append(shape.columns, c[2])
	}
	f := readFrom.FindStringSubmatch(text[m[1]:])
	if f == nil {
		return shape, errors.New("reads no table after FROM; a returning read reads one table")
	}
	shape.table = f[1]
	if !slices.Contains(fromKeywords, strings.ToLower(f[2])) {
		shape.alias = f[2]
	}
	return shape, nil
}

// readsTarget checks that a returning read reads the table its command
// changes: the read's FROM table is the command's target, compared as
// written, and a qualifier on its columns is that table's alias or its name.
func readsTarget(readName string, shape readShape, target string) error {
	if shape.table != target {
		return fmt.Errorf("returning read %q reads %q; the command changes %q", readName, shape.table, target)
	}
	if q := shape.qualifier; q != "" {
		bare := shape.table[strings.LastIndex(shape.table, ".")+1:]
		if q != shape.alias && q != shape.table && q != bare {
			return fmt.Errorf("returning read %q qualifies its columns %q; it reads %q as %q", readName, q, shape.table, cmp.Or(shape.alias, shape.table))
		}
	}
	return nil
}

// resolveReturning resolves a command's returning declaration against the
// statements compiled beside it. It checks the command's verb and the named
// read and its shape, that the read reads the table the command changes,
// and, when d implements Returner and accepts, compiles
// the single-statement form, its parameters rewritten as the command's are.
// sources holds, by name, every statement's body after include expansion
// and its returning declaration.
func resolveReturning(st *Statement, sources map[string]source, stmts map[string]Statement, d sqlate.Dialect) error {
	readName, body := sources[st.name].returning, sources[st.name].body
	if st.tier != TierStandard {
		return errors.New("a returning command is standard tier; a native statement returns its row in its own text")
	}
	if len(st.key) > 0 || len(st.fields) > 0 {
		return errors.New("a returning command is not a projection base; it declares no key or field")
	}
	verb, target, err := commandVerbOf(body)
	if err != nil {
		return err
	}
	read, ok := stmts[readName]
	if !ok {
		return fmt.Errorf("returning read %q is not a statement of this directory", readName)
	}
	if sources[readName].returning != "" {
		return fmt.Errorf("returning read %q declares returning itself", readName)
	}
	if len(read.key) > 0 || len(read.fields) > 0 {
		return fmt.Errorf("returning read %q declares a key or field; it is a read of one row, not a projection base", readName)
	}
	if read.txRequired {
		return fmt.Errorf("returning read %q requires a transaction; the command's own session runs it", readName)
	}
	own := st.Params()
	for _, p := range read.Params() {
		if !slices.Contains(own, p) {
			return fmt.Errorf("returning read %q takes parameter %q the command does not", readName, p)
		}
	}
	shape, err := readShapeOf(sources[readName].body)
	if err != nil {
		return fmt.Errorf("returning read %q %w", readName, err)
	}
	if err := readsTarget(readName, shape, target); err != nil {
		return err
	}
	columns := shape.columns
	form := &returnForm{read: read, verb: verb, columns: columns}
	if r, ok := d.(Returner); ok {
		if text, ok := r.Returning(verb, body, slices.Clone(columns)); ok {
			if strings.Count(text, opener) > strings.Count(body, opener) {
				return fmt.Errorf("dialect %s: its returning form introduces %s; a dialect writes no parameter", d.Name(), opener)
			}
			native, err := rewrite(text, d.Placeholder)
			if err != nil {
				return fmt.Errorf("dialect %s: its returning form: %w", d.Name(), err)
			}
			form.native = &native
			if native.template != nil {
				form.renderings = &sync.Map{}
			}
		}
	}
	st.returning = form
	return nil
}

// Returning is a returning command bound to the scan of its read: the
// handle that runs the command and returns the row as it stands afterward.
// It is a value, built once in a constructor and held.
type Returning[T any] struct {
	cmd  Statement
	read Rows[T]
}

// Returning binds the statement, a returning command, to scan, the scan
// function of the read its returning declaration names; the read's SELECT
// list is the scan order on both forms. A statement that declares no returning is a
// defect in the caller's constructor and panics.
func (st Statement) Returning[T any](scan ScanFunc[T]) Returning[T] {
	if st.returning == nil {
		panic(fmt.Sprintf("query: %s: declares no returning; a returning handle binds a returning command", st.name))
	}
	return Returning[T]{cmd: st, read: st.returning.read.Scan(scan)}
}

// Statement returns the command.
func (r Returning[T]) Statement() Statement { return r.cmd }

// One runs the command and returns the row as it stands afterward, and
// whether the command changed it. When the command changed no row, One
// reads the row as it is and returns changed=false; no row at all is
// sql.ErrNoRows, unmapped. A command that changed more than one row, or
// whose read finds no row after the command changed one, is ErrNotOneRow.
// A command headed "transaction: required" is ErrTransactionRequired
// outside a *sqlate.Tx, on either form, before any SQL runs.
//
// The single-statement form is one query on s, atomic by itself. When it
// returns a second row, that row has already changed, and only the
// caller's transaction can undo the change. The fallback runs the command
// and then its read as one unit: inside s when s is a *sqlate.Tx; inside a
// transaction One opens, commits, and rolls back on any error when s is a
// sqlate.Beginner, as sqlate.Transact runs a unit; and on any other session
// One returns ErrTransactionRequired.
//
// The two forms return the same row for a sound declaration: a read that
// selects exactly the row the command changed, from the command's own
// table, which the load checks as far as the table. They differ when the
// command changes more than one row outside a transaction (the
// single-statement form's change has committed before ErrNotOneRow; the
// fallback's own transaction rolls back), when the read does not find the
// changed row (the single-statement form returns the RETURNING row; the
// fallback returns ErrNotOneRow), and on a session that is neither a
// *sqlate.Tx nor a sqlate.Beginner, where only the single-statement form
// runs.
func (r Returning[T]) One(ctx context.Context, s sqlate.Session, args Args) (T, bool, error) {
	var zero T
	if r.cmd.txRequired {
		if _, ok := s.(*sqlate.Tx); !ok {
			return zero, false, ErrTransactionRequired
		}
	}
	if r.cmd.returning.native != nil {
		return r.single(ctx, s, args)
	}
	switch s := s.(type) {
	case *sqlate.Tx:
		return r.fallback(ctx, s, args)
	case sqlate.Beginner:
		out, err := sqlate.Transact(ctx, s, func(tx *sqlate.Tx) (changedRow[T], error) {
			row, changed, err := r.fallback(ctx, tx, args)
			return changedRow[T]{row: row, changed: changed}, err
		})
		return out.row, out.changed, err
	}
	return zero, false, ErrTransactionRequired
}

// single runs the single-statement form. A returned row is the changed
// row; when none is returned, the read reads the row as it is.
func (r Returning[T]) single(ctx context.Context, s sqlate.Session, args Args) (T, bool, error) {
	var zero T
	form := r.cmd.returning
	text, values, err := r.cmd.bindForm(*form.native, form.renderings, s, args)
	if err != nil {
		return zero, false, err
	}
	rows, err := s.QueryContext(ctx, text, values...)
	if err != nil {
		return zero, false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return zero, false, mapErr(s, err)
		}
		// Next returning false has closed the row set unless the driver
		// reports a further result set; Close releases it before the read,
		// which may share the connection.
		if err := rows.Close(); err != nil {
			return zero, false, mapErr(s, err)
		}
		row, err := r.read.One(ctx, s, args)
		return row, false, err
	}
	row, err := r.read.scan(rows)
	if err != nil {
		return zero, false, mapErr(s, err)
	}
	if rows.Next() {
		return zero, false, fmt.Errorf("%w: %s changed more than one row", ErrNotOneRow, r.cmd.name)
	}
	if err := rows.Err(); err != nil {
		return zero, false, mapErr(s, err)
	}
	return row, true, nil
}

// fallback runs the command and then its read, on tx.
func (r Returning[T]) fallback(ctx context.Context, tx *sqlate.Tx, args Args) (T, bool, error) {
	var zero T
	n, err := r.cmd.Exec(ctx, tx, args)
	if err != nil {
		return zero, false, err
	}
	if n > 1 {
		return zero, false, fmt.Errorf("%w: %s changed %d rows", ErrNotOneRow, r.cmd.name, n)
	}
	row, err := r.read.One(ctx, tx, args)
	if errors.Is(err, sql.ErrNoRows) && n == 1 {
		return zero, false, fmt.Errorf("%w: %s changed a row its read %s does not find", ErrNotOneRow, r.cmd.name, r.read.stmt.name)
	}
	if err != nil {
		return zero, false, err
	}
	return row, n == 1, nil
}

// changedRow is the fallback's outcome carried out of the transaction One
// opens: the row and whether the command changed it.
type changedRow[T any] struct {
	row     T
	changed bool
}

// Guarded binds the handle, a command whose WHERE names the key and the
// expected version, to the optimistic-concurrency protocol: version names
// the command's parameter the expected version binds to, and current reads
// a row's own version. A version that is not a parameter of the command is
// a defect in the caller's constructor and panics.
func (r Returning[T]) Guarded(version string, current func(T) int64) RowGuard[T] {
	if !slices.Contains(r.cmd.Params(), version) {
		panic(fmt.Sprintf("query: %s: version %q is not a parameter of the command", r.cmd.name, version))
	}
	return RowGuard[T]{returning: r, version: version, current: current}
}
