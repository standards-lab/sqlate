package query

import (
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

// Verb is the data-changing statement a returning clause attaches to.
type Verb string

const (
	// Insert is a command whose body starts INSERT INTO.
	Insert Verb = "INSERT"
	// Update is a command whose body starts UPDATE.
	Update Verb = "UPDATE"
)

// Returner is the dialect capability that makes a data-changing statement
// return its row. body is the statement after include expansion, its
// parameters still {{…}} slots; columns are the bare column names in scan
// order. ok=false declines, and the library runs the command and then its
// read. An implementation declines a Verb it does not know.
type Returner interface {
	Returning(verb Verb, body string, columns []string) (text string, ok bool)
}

// returnForm is a command's returning declaration, resolved: the read that
// reads the changed row back, which is also the fallback's second step, the
// command's verb, the read's bare column names in scan order, and the
// single-statement form the dialect rendered, nil when it declined or does
// not implement Returner. renderings caches the single-statement form's
// expanded text by the lists' lengths, as Statement.renderings does the
// command's own; nil when the form has no expansion or there is no form.
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
	// commandVerb is the leading keyword a returning command starts with.
	commandVerb = regexp.MustCompile(`(?i)^(insert\s+into|update)\s`)
	// readList is the SELECT list of a returning read: everything before its
	// first FROM, each item then checked on its own.
	readList = regexp.MustCompile(`(?is)^select\s+(.+?)\s+from\s`)
	// readColumn is one item of a returning read's SELECT list: a column,
	// optionally qualified by a table or alias.
	readColumn = regexp.MustCompile(`^(?:([a-z_][a-z0-9_]*)\.)?([a-z_][a-z0-9_]*)$`)
)

// stripLeading returns body without its leading whitespace and comments.
func stripLeading(body string) string {
	return body[len(leadingComment.FindString(body)):]
}

// commandVerbOf reads the verb of a returning command's body: INSERT INTO or
// UPDATE, matched case-insensitively. Anything else, DELETE or a leading WITH
// among them, is an error naming what the body starts with.
func commandVerbOf(body string) (Verb, error) {
	rest := stripLeading(body)
	m := commandVerb.FindStringSubmatch(rest)
	if m == nil {
		first, _, _ := strings.Cut(rest, " ")
		first, _, _ = strings.Cut(first, "\n")
		return "", fmt.Errorf("a returning command is an INSERT INTO or an UPDATE; this one starts %q", first)
	}
	if strings.EqualFold(m[1], "update") {
		return Update, nil
	}
	return Insert, nil
}

// readColumns reads the returned columns of a returning read's body: the
// body is SELECT <c>, <c>, … FROM …, each <c> a column or table.column, every
// item under the same qualifier or none, no name twice. The columns are the
// bare names, in the list's order.
func readColumns(body string) ([]string, error) {
	m := readList.FindStringSubmatch(stripLeading(body))
	if m == nil {
		return nil, errors.New("is not SELECT <column>, … FROM …")
	}
	var columns []string
	qualifier := ""
	for i, item := range strings.Split(m[1], ",") {
		item = strings.TrimSpace(item)
		c := readColumn.FindStringSubmatch(item)
		if c == nil {
			return nil, fmt.Errorf("selects %q; each item is a column or table.column", item)
		}
		if i == 0 {
			qualifier = c[1]
		} else if c[1] != qualifier {
			return nil, fmt.Errorf("selects %q; every item has the same qualifier or none", item)
		}
		if slices.Contains(columns, c[2]) {
			return nil, fmt.Errorf("selects %q twice", c[2])
		}
		columns = append(columns, c[2])
	}
	return columns, nil
}

// resolveReturning resolves a command's returning declaration against the
// statements compiled beside it: the command's verb, the named read and its
// shape, and, when d implements Returner and accepts, the single-statement
// form, its parameters rewritten as the command's are. sources holds every
// statement's body after include expansion and returning declaration, by
// name.
func resolveReturning(st *Statement, sources map[string]source, stmts map[string]Statement, d sqlate.Dialect) error {
	readName, body := sources[st.name].returning, sources[st.name].body
	if st.tier != TierStandard {
		return errors.New("a returning command is standard tier; a native statement returns its row in its own text")
	}
	if len(st.key) > 0 || len(st.fields) > 0 {
		return errors.New("a returning command is not a projection base; it declares no key or field")
	}
	verb, err := commandVerbOf(body)
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
	columns, err := readColumns(sources[readName].body)
	if err != nil {
		return fmt.Errorf("returning read %q %w", readName, err)
	}
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
// handle that runs the command and yields the row as it stands afterward.
// It is a value, built once in a constructor and held.
type Returning[T any] struct {
	cmd  Statement
	read Rows[T]
}

// Returning binds the statement, a returning command, to scan, the scan of
// the read its returning declaration names; the read's SELECT list is the
// scan order on both forms. A statement that declares no returning is a
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
// whether the command changed it. A command that changed no row reads the
// row as it is, changed=false; no row at all is sql.ErrNoRows, unmapped. A
// command that changed more than one row, or whose read finds no row after
// one changed, is ErrNotOneRow. A command headed "transaction: required" is
// ErrTransactionRequired outside a *sqlate.Tx, on either form, before any
// SQL runs.
//
// Where the dialect rendered the single-statement form, it is one query on
// s, atomic by itself; a second row it returns has already been changed,
// and only the caller's transaction undoes that. Otherwise the command and
// then its read run as one unit: inside s when s is a *sqlate.Tx, inside a
// transaction One opens, commits, and rolls back on any error when s is a
// sqlate.Beginner, and ErrTransactionRequired on any other session.
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
		return r.owned(ctx, s, args)
	}
	return zero, false, ErrTransactionRequired
}

// single runs the single-statement form: one row is the changed row; no row
// reads the row as it is with the read.
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
		// Closed before the read, which may share the connection.
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

// owned runs the fallback in a transaction of its own, as DB.Transact does:
// commit on success; on an error roll back and return it, a rollback
// failure joined onto it; on a panic roll back and re-panic.
func (r Returning[T]) owned(ctx context.Context, b sqlate.Beginner, args Args) (T, bool, error) {
	var zero T
	tx, err := b.Begin(ctx)
	if err != nil {
		return zero, false, err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	row, changed, err := r.fallback(ctx, tx, args)
	if err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			err = errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return zero, false, err
	}
	if err := tx.Commit(); err != nil {
		return zero, false, err
	}
	return row, changed, nil
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
