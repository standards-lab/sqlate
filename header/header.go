package header

import (
	"fmt"
	"regexp"
	"strings"
)

// Marker opens a declaration line. It is a comment to every engine that
// reads "--" without requiring whitespace after it, and it is never sent:
// the header is the loader's, and a consumer hands the engine the body.
const Marker = "--|"

// Declaration is one "--| key: value" line of the header, with its 1-based
// line number for a consumer's error messages.
type Declaration struct {
	Key   string
	Value string
	Line  int
}

// Header is the declarations of one file, in file order, and where the body
// begins. A key may repeat; "field" does, once per column.
type Header struct {
	declarations []Declaration
	end          int
}

var declaration = regexp.MustCompile(`^([a-z][a-z0-9_-]*):\s*(.*)$`)

// Parse reads the header from text: the leading run of lines that are
// blank, plain "--" comments (prose, skipped), or "--|" declarations. The
// header ends at the first line that is none of those. A declaration line
// that is not "--| key: value", or a declaration after the body has begun,
// is an error.
func Parse(text string) (Header, error) {
	var h Header
	inHeader := true
	offset := 0
	for n := 1; offset < len(text); n++ {
		raw, next := text[offset:], len(text)
		if i := strings.IndexByte(raw, '\n'); i >= 0 {
			raw, next = raw[:i], offset+i+1
		}
		line := strings.TrimSpace(raw)
		if inHeader && line != "" && !strings.HasPrefix(line, "--") {
			inHeader = false
			h.end = offset
		}
		if strings.HasPrefix(line, Marker) {
			if !inHeader {
				return h, fmt.Errorf("header: line %d: declaration after the body", n)
			}
			m := declaration.FindStringSubmatch(strings.TrimSpace(line[len(Marker):]))
			if m == nil {
				return h, fmt.Errorf("header: line %d: %q is not \"--| key: value\"", n, line)
			}
			h.declarations = append(h.declarations, Declaration{Key: m[1], Value: strings.TrimSpace(m[2]), Line: n})
		}
		offset = next
	}
	if inHeader {
		h.end = len(text)
	}
	return h, nil
}

// End returns the byte offset at which the body begins: the start of the
// first line that is neither a comment nor blank, or the length of the text
// when there is no body. A consumer sends text[End():] to the engine and
// scans it for what only the body may contain.
func (h Header) End() int { return h.end }

// Declarations returns every declaration in file order.
func (h Header) Declarations() []Declaration {
	out := make([]Declaration, len(h.declarations))
	copy(out, h.declarations)
	return out
}

// Get returns the value of the first declaration with key, and whether one
// exists.
func (h Header) Get(key string) (string, bool) {
	for _, d := range h.declarations {
		if d.Key == key {
			return d.Value, true
		}
	}
	return "", false
}

// All returns the values of every declaration with key, in order.
func (h Header) All(key string) []string {
	var out []string
	for _, d := range h.declarations {
		if d.Key == key {
			out = append(out, d.Value)
		}
	}
	return out
}

// Keys returns the distinct keys in order of first appearance, so a consumer
// can reject the ones it does not know.
func (h Header) Keys() []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range h.declarations {
		if !seen[d.Key] {
			seen[d.Key] = true
			out = append(out, d.Key)
		}
	}
	return out
}
