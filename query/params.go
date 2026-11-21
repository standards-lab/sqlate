package query

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

var (
	parameter = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*(\.\.\.)?\s*(?::\s*(` + typeToken + `)\s*)?\}\}`)
	opener    = "{{"
)

const typeToken = `[A-Za-z_][A-Za-z0-9_ ]*?(?:\([0-9]+(?:\s*,\s*[0-9]+)?\))?`

var sqlType = regexp.MustCompile(`^` + typeToken + `$`)

type param struct {
	name   string
	expand bool
}

type occurrence struct {
	before string
	index  int
	typ    string
}

type compiled struct {
	text     string
	params   []param
	template []occurrence
	tail     string
}

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

func malformed(body string, at int) error {
	rest := body[at:]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[:i]
	}
	return fmt.Errorf("malformed parameter %q: want {{name}}, {{name:type}}, or {{name...}}", rest)
}
