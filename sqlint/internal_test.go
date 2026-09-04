package sqlint

import "testing"

func TestMatch_GlobsSegments(t *testing.T) {
	cases := []struct {
		glob, dir string
		want      bool
	}{
		{"**/statements", "domain/a/statements", true},
		{"**/statements", "statements", true},
		{"**/statements", "domain/a/statements/x", false},
		{"domain/*/statements", "domain/a/statements", true},
		{"domain/*/statements", "domain/a/b/statements", false},
		{"query/patterns", "query/patterns", true},
		{"admin/**", "admin/database/migrations", true},
		{"admin/**", "domain", false},
	}
	for _, c := range cases {
		if got := match(c.glob, c.dir); got != c.want {
			t.Errorf("match(%q, %q) = %v", c.glob, c.dir, got)
		}
	}
}

func TestIsModulePath(t *testing.T) {
	for p, want := range map[string]bool{"github.com/x/y": true, "query": false, "admin/database": false, "example.org": true, "a.b/c": true} {
		if isModulePath(p) != want {
			t.Errorf("isModulePath(%q) != %v", p, want)
		}
	}
}
