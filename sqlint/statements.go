package sqlint

import (
	"io/fs"
	"path"
	"regexp"
	"strings"

	"github.com/standards-lab/sqlate/header"
)

// verbNamed catches a file named for its SQL verb rather than its
// operation; delete is both a verb and a command and is allowed.
var verbNamed = regexp.MustCompile(`^(insert|select|update|upsert|merge)(_|\.)`)

// lintStatements compiles a statement directory the way a program does,
// against the resolved catalog, then applies the rules the compiler
// leaves to review.
func (l *linter) lintStatements(dir string, on map[string]bool) {
	if l.catalog != nil {
		if _, err := l.catalog.Compile(l.fsys, dir, stubDialect{}); err != nil {
			l.report(dir, 0, err.Error())
		}
	}
	l.lintFiles(dir, on)
}

// lintFiles applies the per-file rules of a statement or pattern
// directory under its switches.
func (l *linter) lintFiles(dir string, on map[string]bool) {
	entries, _ := fs.ReadDir(l.fsys, dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		p := path.Join(dir, e.Name())
		if on["verb_named"] && verbNamed.MatchString(e.Name()) {
			l.report(p, 0, "named for its SQL verb; name a statement for its operation")
		}
		text, _ := fs.ReadFile(l.fsys, p)
		h, err := header.Parse(string(text))
		if err != nil {
			continue // reported by the compile or the catalog
		}
		tier, _ := h.Get("tier")
		l.lintBody(p, string(text), h.End(), on["delimiter"], on["native_forms"] && tier == "standard", on["guard"])
	}
}
