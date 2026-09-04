package sqlint

import (
	"io/fs"
	"path"
	"strings"

	"github.com/standards-lab/sqlate/header"
)

// lintMigrations checks each migration's header and, for a
// non-transactional file, that it contains exactly one statement.
func (l *linter) lintMigrations(dir string, on map[string]bool) {
	entries, _ := fs.ReadDir(l.fsys, dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		p := path.Join(dir, e.Name())
		text, _ := fs.ReadFile(l.fsys, p)
		h, err := header.Parse(string(text))
		if err != nil {
			l.report(p, 0, err.Error())
			continue
		}
		if v, ok := h.Get("transaction"); ok && v == "none" && on["single_statement"] {
			body := strings.TrimRight(strings.TrimSpace(string(text)[h.End():]), ";")
			if strings.Contains(body, ";") {
				l.report(p, 0, "a non-transactional migration contains exactly one statement")
			}
		}
	}
}
