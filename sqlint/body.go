package sqlint

import (
	"fmt"
	"regexp"
	"strings"
)

var guardInclude = regexp.MustCompile(`\{\{>\s*[a-z_][a-z0-9_]*\.(guard_where|guard_set)\s*\}\}`)

var setClause = regexp.MustCompile(`(?i)\bSET\b`)

func (l *linter) lintBody(p, text string, end int, delimiter, forms, guard bool) {
	line := 1 + strings.Count(text[:end], "\n")
	inBlock := false
	var firstGuardWhereLine, firstGuardSetLine int
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
					l.report(p, line, fmt.Sprintf("%q (%s) in a standard-tier file; declare the tier native and name the port", m, f.label()))
				}
			}
		}
		if guard {
			for _, m := range guardInclude.FindAllStringSubmatch(code, -1) {
				switch {
				case m[1] == "guard_where" && firstGuardWhereLine == 0:
					firstGuardWhereLine = line
				case m[1] == "guard_set" && firstGuardSetLine == 0:
					firstGuardSetLine = line
				}
			}
			hasSet = hasSet || setClause.MatchString(code)
		}
		line++
	}
	if firstGuardWhereLine > 0 && firstGuardSetLine == 0 && hasSet {
		l.report(p, firstGuardWhereLine, "includes guard_where in a SET statement without guard_set; a guarded command advances the version it checks")
	}
	if firstGuardSetLine > 0 && firstGuardWhereLine == 0 {
		l.report(p, firstGuardSetLine, "includes guard_set without guard_where; a guarded command checks the version it advances")
	}
}

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

func (f form) label() string {
	if f.from == "" {
		return f.name
	}
	return f.name + ", from " + f.from
}
