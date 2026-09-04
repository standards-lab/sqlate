package sqlint

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
)

// Resolver turns a module path into the filesystem of the module's root
// directory, where its sqlint.toml is read. A nil Resolver refuses every
// module path.
type Resolver func(module string) (fs.FS, error)

// GoList resolves a module path through go list -m in the module at root,
// to the directory of the version its go.mod pins, a workspace or replace
// directive included, so a source's files are the ones the runtime
// embeds.
func GoList(root string) Resolver {
	return func(module string) (fs.FS, error) {
		cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list -m %s: %w", module, err)
		}
		dir := strings.TrimSpace(string(out))
		if dir == "" {
			return nil, fmt.Errorf("go list -m %s: no directory; is the module required?", module)
		}
		return os.DirFS(dir), nil
	}
}
