package sqlint

import (
	"fmt"
	"regexp"
	"strings"
)

// guardInclude matches an include of one of the guard protocol's two
// patterns under any namespace, the way the loader parses an include; the
// library publishes them, and an engine overlay or another source may
// respell them, so the name and not the namespace identifies them.
var guardInclude = regexp.MustCompile(`\{\{>\s*[a-z_][a-z0-9_]*\.(guard_where|guard_set)\s*\}\}`)

// setClause matches the SET keyword, which marks a statement as one that
// assigns columns: the guarded command, and not the guard's check or a
// guarded delete, neither of which has a version to advance.
var setClause = regexp.MustCompile(`(?i)\bSET\b`)

// lintBody checks the body's lines: the reserved delimiter in a comment or
// a literal, the engine's native forms in a standard-tier file, and the
// guard protocol's two includes appearing together. A form is matched
// against the line's code only, string literals and the comment tail
// stripped first, because whether text is data or syntax is a property of
// SQL, not of the engine, and a per-line expression cannot track quote
// state. The guard check spans the body, since the includes sit on
// different lines of a command, and reports on the include that is
// present.
func (l *linter) lintBody(p, text string, end int, delimiter, forms, guard bool) {
	line := 1 + strings.Count(text[:end], "\n")
	inBlock := false
	var whereLine, setLine int // the first guard_where and guard_set includes, 0 for none
	hasSet := false
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
		if guard {
			for _, m := range guardInclude.FindAllStringSubmatch(code, -1) {
				switch {
				case m[1] == "guard_where" && whereLine == 0:
					whereLine = line
				case m[1] == "guard_set" && setLine == 0:
					setLine = line
				}
			}
			hasSet = hasSet || setClause.MatchString(code)
		}
		line++
	}
	// A guarded command advances the version it checks and checks the
	// version it advances; the guard's check and a guarded delete include
	// the predicate alone, and have no SET list.
	if whereLine > 0 && setLine == 0 && hasSet {
		l.report(p, whereLine, "includes guard_where in a SET statement without guard_set; a guarded command advances the version it checks")
	}
	if setLine > 0 && whereLine == 0 {
		l.report(p, setLine, "includes guard_set without guard_where; a guarded command checks the version it advances")
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
