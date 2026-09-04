package sqlint

import "github.com/standards-lab/sqlate/query"

// lintPatterns validates a pattern directory as a catalog source (tier
// declared, a port named for a native pattern, slots only), then applies
// the body rules.
func (l *linter) lintPatterns(dir string, on map[string]bool) {
	if _, err := query.NewCatalog(query.Publish("lint", l.fsys, dir)); err != nil {
		l.report(dir, 0, err.Error())
	}
	l.lintFiles(dir, on)
}
