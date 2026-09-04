package sqlint

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/standards-lab/sqlate/query"
)

// Finding is one convention a file fails: its path, the 1-based line when
// the check has one (0 otherwise), and the message.
type Finding struct {
	Path    string
	Line    int
	Message string
}

// String renders the finding as path:line: message, or path: message when
// the check has no line.
func (f Finding) String() string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", f.Path, f.Line, f.Message)
	}
	return fmt.Sprintf("%s: %s", f.Path, f.Message)
}

// linter is one run over one tree.
type linter struct {
	fsys     fs.FS
	cfg      *Config
	resolve  Resolver
	catalog  *query.Catalog
	forms    []form
	findings []Finding
}

// form is one native form the engine declared: the name the finding
// reports and the expression that recognizes it.
type form struct {
	name string
	re   *regexp.Regexp
}

// Lint walks fsys under cfg and returns every finding, in walk order. A
// nil cfg is the defaults. resolve turns a module path into its root
// filesystem; nil refuses every module path. A source or engine that does
// not resolve is a finding against File; the rest of the tree is linted
// with what did.
func Lint(fsys fs.FS, cfg *Config, resolve Resolver) []Finding {
	if cfg == nil {
		cfg = defaults()
	}
	if resolve == nil {
		resolve = func(module string) (fs.FS, error) { return nil, fmt.Errorf("no module resolution for %s", module) }
	}
	l := &linter{fsys: fsys, cfg: cfg, resolve: resolve}
	l.resolveSources()
	l.resolveEngine()
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && p != "." {
			return fs.SkipDir
		}
		if on, ok := cfg.Roles["statements"].switches(p); ok {
			l.lintStatements(p, on)
		}
		if on, ok := cfg.Roles["patterns"].switches(p); ok {
			l.lintPatterns(p, on)
		}
		if on, ok := cfg.Roles["migrations"].switches(p); ok {
			l.lintMigrations(p, on)
		}
		return nil
	})
	return l.findings
}

func (l *linter) report(p string, line int, msg string) {
	l.findings = append(l.findings, Finding{Path: p, Line: line, Message: msg})
}

// locate turns a source or engine value into a filesystem and a base
// path: a module path through the resolver, anything else a directory of
// the tree.
func (l *linter) locate(value string) (fs.FS, string, error) {
	if isModulePath(value) {
		fsys, err := l.resolve(value)
		return fsys, ".", err
	}
	if _, err := fs.Stat(l.fsys, value); err != nil {
		return nil, "", err
	}
	return l.fsys, value, nil
}

// resolveSources builds the catalog the statement directories compile
// against: each namespace's source, and for the library its engine
// overlay. A producer, a module or a directory that contains its own
// configuration, names its pattern directory in its export; a bare
// directory is the pattern files themselves. A source that does not
// resolve is a finding against the configuration; the catalog is built
// from the rest.
func (l *linter) resolveSources() {
	var sources []query.Source
	names := make([]string, 0, len(l.cfg.Sources))
	for ns := range l.cfg.Sources {
		names = append(names, ns)
	}
	sort.Strings(names)
	for _, ns := range names {
		s := l.cfg.Sources[ns]
		fsys, base, err := l.locate(s.Path)
		if err != nil {
			l.report(File, 0, fmt.Sprintf("sources.%s: %v", ns, err))
			continue
		}
		dir := base
		if isProducer(fsys, base) {
			export, err := readExport(fsys, base)
			if err != nil {
				l.report(File, 0, fmt.Sprintf("sources.%s: %v", ns, err))
				continue
			}
			if export.Patterns == "" {
				l.report(File, 0, fmt.Sprintf("sources.%s: %s exports no patterns", ns, s.Path))
				continue
			}
			dir = path.Join(base, export.Patterns)
		}
		src := query.Publish(ns, fsys, dir)
		if s.Overlay != "" {
			ofs, obase, err := l.locate(s.Overlay)
			if err != nil {
				l.report(File, 0, fmt.Sprintf("sources.%s.overlay: %v", ns, err))
				continue
			}
			odir := obase
			if isProducer(ofs, obase) {
				oexport, err := readExport(ofs, obase)
				if err != nil {
					l.report(File, 0, fmt.Sprintf("sources.%s.overlay: %v", ns, err))
					continue
				}
				if oexport.Overlay == "" {
					l.report(File, 0, fmt.Sprintf("sources.%s.overlay: %s exports no overlay", ns, s.Overlay))
					continue
				}
				odir = path.Join(obase, oexport.Overlay)
			}
			src = src.Overlay(ofs, odir)
		}
		sources = append(sources, src)
	}
	catalog, err := query.NewCatalog(sources...)
	if err != nil {
		l.report(File, 0, err.Error())
		return
	}
	l.catalog = catalog
}

// resolveEngine reads the native forms the configured engine declares and
// compiles each; an engine is always a producer, since a consumer never
// defines one. A malformed expression is a finding against the
// configuration, before any file is read.
func (l *linter) resolveEngine() {
	if l.cfg.Engine == "" {
		return
	}
	fsys, base, err := l.locate(l.cfg.Engine)
	if err != nil {
		l.report(File, 0, fmt.Sprintf("engine: %v", err))
		return
	}
	export, err := readExport(fsys, base)
	if err != nil {
		l.report(File, 0, fmt.Sprintf("engine: %v", err))
		return
	}
	names := make([]string, 0, len(export.NativeForms))
	for name := range export.NativeForms {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		re, err := regexp.Compile(export.NativeForms[name])
		if err != nil {
			l.report(File, 0, fmt.Sprintf("engine: native_forms.%s: %v", name, err))
			continue
		}
		l.forms = append(l.forms, form{name: name, re: re})
	}
}
