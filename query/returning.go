package query

import (
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
