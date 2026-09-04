package query

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// A parameter is written {{name}}, or {{name:type}} to bind it through
// CAST to an SQL type — standard or the engine's own, as the file's tier
// declares; the type is the author's and reaches the engine verbatim, where
// Verify catches a name the engine does not know. {{name...}} and
// {{name...:type}} expand: the argument is a non-empty slice, and the
// parameter renders as one placeholder per element, so an IN list binds
// as values and never as text. Whitespace inside the braces is allowed.
// The delimiter is reserved: it means a parameter wherever it appears in
// the body, string literals and comments included, so the body needs no
// lexer, and a {{ that does not form a parameter is a load error.
var (
	parameter = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*(\.\.\.)?\s*(?::\s*(` + typeToken + `)\s*)?\}\}`)
	opener    = "{{"
)

// A pattern include is written {{> name}} and is resolved at load, before
// the parameters are; see patterns.go.

// typeToken is an SQL type as written in a cast or a field contract: a
// name, optionally with a length or precision, such as text, uuid,
// numeric(12,2), or varchar(200).
const typeToken = `[A-Za-z_][A-Za-z0-9_ ]*?(?:\([0-9]+(?:\s*,\s*[0-9]+)?\))?`

var sqlType = regexp.MustCompile(`^` + typeToken + `$`)

// param is one declared parameter: its name and whether it expands. A
// name that occurs with and without expansion is a load error: a parameter
// has one arity. The type is the occurrence's, so a name may cast where it
// needs to and bind plain elsewhere.
type param struct {
	name   string
	expand bool
}

// occurrence is one place a parameter appears in the body, in the
// template an expanded statement renders at bind: the text before it, the
// parameter's index, and the type this occurrence casts through (empty
// for none).
type occurrence struct {
	before string
	index  int
	typ    string
}

// compiled is the rewrite's result. For a statement with no expansion, text
// is final and the parameters' positions are their first occurrences. For
// an expanded statement the text depends on each list's length, so the
// body is kept as occurrences and rendered at bind; text is then the
// arity-one rendering, what Verify prepares and Text reports.
type compiled struct {
	text     string
	params   []param
	template []occurrence
	tail     string
}

// rewrite resolves the parameters of body to the dialect's positional
// placeholders. Each distinct name takes the position of its first
// occurrence; later occurrences rebind to it; an expanded name takes as
// many consecutive positions as its list has elements.
func rewrite(body string, placeholder func(int) string) (compiled, error) {
	var c compiled
	index := map[string]int{}
	last := 0
	expanded := false
	for _, m := range parameter.FindAllStringSubmatchIndex(body, -1) {
		if stray := strings.Index(body[last:m[0]], opener); stray >= 0 {
			return c, malformed(body, last+stray)
		}
		p := param{name: body[m[2]:m[3]], expand: m[4] >= 0}
		o := occurrence{before: body[last:m[0]]}
		if m[6] >= 0 {
			o.typ = strings.TrimSpace(body[m[6]:m[7]])
		}
		i, ok := index[p.name]
		if !ok {
			i = len(c.params)
			index[p.name] = i
			c.params = append(c.params, p)
		} else if c.params[i].expand != p.expand {
			return c, fmt.Errorf("parameter %q occurs both expanded and not; a parameter has one arity", p.name)
		}
		expanded = expanded || p.expand
		o.index = i
		c.template = append(c.template, o)
		last = m[1]
	}
	if stray := strings.Index(body[last:], opener); stray >= 0 {
		return c, malformed(body, last+stray)
	}
	c.tail = body[last:]
	c.text = c.render(placeholder, func(int) int { return 1 })
	if !expanded {
		c.template = nil
	}
	return c, nil
}

// render writes the body with placeholders assigned in first-occurrence
// order, an expanded parameter taking arity(i) consecutive positions.
func (c compiled) render(placeholder func(int) string, arity func(int) int) string {
	var out strings.Builder
	start := make([]int, len(c.params))
	next := 1
	for i, p := range c.params {
		start[i] = next
		if p.expand {
			next += arity(i)
		} else {
			next++
		}
	}
	for _, o := range c.template {
		out.WriteString(o.before)
		p := c.params[o.index]
		n := 1
		if p.expand {
			n = arity(o.index)
		}
		for k := range n {
			if k > 0 {
				out.WriteString(", ")
			}
			if o.typ != "" {
				out.WriteString("CAST(")
				out.WriteString(placeholder(start[o.index] + k))
				out.WriteString(" AS ")
				out.WriteString(o.typ)
				out.WriteString(")")
			} else {
				out.WriteString(placeholder(start[o.index] + k))
			}
		}
	}
	out.WriteString(c.tail)
	return out.String()
}

// arities reads each expanded parameter's list length from args, and
// returns the cache key naming them. A missing argument, a non-slice, or an
// empty list is an ArgumentError.
func (c compiled) arities(statement string, args Args) ([]int, string, error) {
	n := make([]int, len(c.params))
	var key strings.Builder
	for i, p := range c.params {
		if !p.expand {
			continue
		}
		v, ok := args[p.name]
		if !ok {
			return nil, "", &ArgumentError{Statement: statement, Name: p.name}
		}
		rv := reflect.ValueOf(v)
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			return nil, "", &ArgumentError{Statement: statement, Name: p.name, Problem: "an expanded parameter takes a slice"}
		}
		if rv.Len() == 0 {
			return nil, "", &ArgumentError{Statement: statement, Name: p.name, Problem: "an expanded parameter takes a non-empty list"}
		}
		n[i] = rv.Len()
		key.WriteString(strconv.Itoa(rv.Len()))
		key.WriteByte(',')
	}
	return n, key.String(), nil
}

// malformed reports a {{ that does not open a parameter, quoting the text
// from it to the end of its line.
func malformed(body string, at int) error {
	rest := body[at:]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[:i]
	}
	return fmt.Errorf("malformed parameter %q: want {{name}}, {{name:type}}, or {{name...}}", rest)
}
