package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/standards-lab/sqlate/sqlint"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	fsys := os.DirFS(root)
	var lines []string
	cfg, err := sqlint.Load(fsys)
	if err != nil {
		// Each line of a configuration error begins with the file's name.
		lines = append(lines, strings.Split(err.Error(), "\n")...)
	}
	if cfg != nil {
		for _, f := range sqlint.Lint(fsys, cfg, sqlint.GoList(root)) {
			lines = append(lines, f.String())
		}
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println(l)
	}
	if len(lines) > 0 {
		fmt.Fprintf(os.Stderr, "sqlint: %d finding(s)\n", len(lines))
		os.Exit(1)
	}
	fmt.Println("sqlint: ok")
}
