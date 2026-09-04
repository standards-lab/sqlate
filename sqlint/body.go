package sqlint

import (
	"fmt"
	"strings"
)

// lintBody checks the body's lines: the reserved delimiter in a comment or
// a literal, and the engine's native forms in a standard-tier file. A
// form is matched against the line's code only, string literals and the
// comment tail stripped first, because whether text is data or syntax is
// a property of SQL, not of the engine, and a per-line expression cannot
// track quote state.
func (l *linter) lintBody(p, text string, end int, delimiter, forms bool) {
	line := 1 + strings.Count(text[:end], "\n")
	inBlock := false
	for ln := range strings.SplitSeq(text[end:], "\n") {
		if delimiter {
			if i := strings.Index(ln, "--"); i >= 0 && strings.Contains(ln[i:], "{{") {
				l.report(p, line, "{{ inside a comment: the delimiter is reserved for parameters")
			}
			if inLiteral(ln) {
				l.report(p, line, "{{ inside a string literal: the delimiter is reserved for parameters")
			}
		}
		var code string
		code, inBlock = codeOnly(ln, inBlock)
		if forms {
			for _, f := range l.forms {
				if m := f.re.FindString(code); m != "" {
					l.report(p, line, fmt.Sprintf("%q (%s) in a standard-tier file; declare the tier native and name the port", m, f.name))
				}
			}
		}
		line++
	}
}

// inLiteral reports a {{ inside a single-quoted literal on one line,
// tracking the quote state so a closed literal followed by a parameter is
// not one.
func inLiteral(line string) bool {
	open := false
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\'':
			open = !open
		case open && strings.HasPrefix(line[i:], "{{"):
			return true
		}
	}
	return false
}

// codeOnly returns the line with every single-quoted literal and
// double-quoted identifier emptied, the line-comment tail removed, and
// block-comment text removed (inBlock records an open block comment
// across lines), so a form matches syntax and never data, a name, or
// prose. A quote doubled inside a literal ('it”s') closes and reopens,
// which empties it all the same.
func codeOnly(line string, inBlock bool) (string, bool) {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		switch {
		case inBlock:
			if strings.HasPrefix(line[i:], "*/") {
				inBlock = false
				i++
			}
		case quote != 0:
			if line[i] == quote {
				quote = 0
				b.WriteByte(line[i])
			}
		case line[i] == '\'' || line[i] == '"':
			quote = line[i]
			b.WriteByte(line[i])
		case strings.HasPrefix(line[i:], "--"):
			return b.String(), inBlock
		case strings.HasPrefix(line[i:], "/*"):
			inBlock = true
			i++
		default:
			b.WriteByte(line[i])
		}
	}
	return b.String(), inBlock
}
