package sqlint

import (
	"io/fs"
	"path"
	"regexp"
	"strings"

	"github.com/standards-lab/sqlate/header"
)

var verbNamed = regexp.MustCompile(`^(insert|select|update|upsert|merge)(_|\.)`)

func (l *linter) lintStatements(dir string, on map[string]bool) {
	if l.catalog != nil {
		if _, err := l.catalog.Compile(l.fsys, dir, stubDialect{}); err != nil {
			l.report(dir, 0, err.Error())
		}
	}
	l.lintFiles(dir, on)
}

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
			continue
		}
		tier, _ := h.Get("tier")
		l.lintBody(p, string(text), h.End(), on["delimiter"], on["native_forms"] && tier == "standard", on["guard"])
	}
}
