package sqlint

import (
	"path"
	"strings"
)

// match reports whether the slash-separated glob matches the directory
// path: each segment is a path.Match pattern, and "**" matches any run of
// segments, including none.
func match(glob, dir string) bool {
	return matchSegments(strings.Split(glob, "/"), strings.Split(path.Clean(dir), "/"))
}

func matchSegments(gs, ps []string) bool {
	if len(gs) == 0 {
		return len(ps) == 0
	}
	if gs[0] == "**" {
		for i := 0; i <= len(ps); i++ {
			if matchSegments(gs[1:], ps[i:]) {
				return true
			}
		}
		return false
	}
	if len(ps) == 0 {
		return false
	}
	ok, err := path.Match(gs[0], ps[0])
	return err == nil && ok && matchSegments(gs[1:], ps[1:])
}
