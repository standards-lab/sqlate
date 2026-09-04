package migrate

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"

	"github.com/standards-lab/sqlate/header"
)

// Migration is one schema step: the version and name that identify it in
// the history, the SQL that applies and reverts it, and whether it runs in a
// transaction. Down may be empty.
type Migration struct {
	Version       int
	Name          string
	Up            string
	Down          string
	Transactional bool
}

var fileName = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_-]+)\.(up|down)\.sql$`)

// Files reads the NNNN_name.{up,down}.sql layout under dir in fsys into a
// version-ordered set. The up file's "--|" header decides Transactional:
// the "transaction" declaration, "none" opting out and "required" or absence
// keeping the transaction; a down file that declares differently is an
// error. Up and Down are the files' bodies; the engine never sees a header.
// Versions must be unique; a down without its up is an error; an up without
// its down is allowed.
func Files(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: read %s: %w", dir, err)
	}
	byVersion := map[int]*Migration{}
	downMode := map[int]*bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migrate: %s: name is not NNNN_name.{up,down}.sql", e.Name())
		}
		version, err := strconv.Atoi(m[1])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("migrate: %s: version must be a positive integer", e.Name())
		}
		text, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", e.Name(), err)
		}
		mig := byVersion[version]
		if mig == nil {
			mig = &Migration{Version: version, Name: m[2], Transactional: true}
			byVersion[version] = mig
		} else if mig.Name != m[2] {
			return nil, fmt.Errorf("migrate: version %d has two names: %q and %q", version, mig.Name, m[2])
		}
		transactional, body, err := declarations(string(text))
		if err != nil {
			return nil, fmt.Errorf("migrate: %s: %w", e.Name(), err)
		}
		switch m[3] {
		case "up":
			if mig.Up != "" {
				return nil, fmt.Errorf("migrate: version %d has two up files", version)
			}
			mig.Up = body
			mig.Transactional = transactional
		case "down":
			if mig.Down != "" {
				return nil, fmt.Errorf("migrate: version %d has two down files", version)
			}
			mig.Down = body
			downMode[version] = &transactional
		}
	}
	out := make([]Migration, 0, len(byVersion))
	for v, mig := range byVersion {
		if mig.Up == "" {
			return nil, fmt.Errorf("migrate: version %d has a down file but no up file", v)
		}
		if d := downMode[v]; d != nil && *d != mig.Transactional {
			return nil, fmt.Errorf("migrate: version %d: up and down disagree on the transaction header", v)
		}
		out = append(out, *mig)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// declarations reads a file's header and returns whether the migration runs in a
// transaction ("none" opts out, "required" or no declaration keeps it, any
// other value is an error) and the body the engine receives.
func declarations(text string) (transactional bool, body string, err error) {
	h, err := header.Parse(text)
	if err != nil {
		return false, "", err
	}
	body = text[h.End():]
	switch v, ok := h.Get("transaction"); {
	case !ok, v == "required":
		return true, body, nil
	case v == "none":
		return false, body, nil
	default:
		return false, "", fmt.Errorf("transaction declaration %q is not required or none", v)
	}
}
