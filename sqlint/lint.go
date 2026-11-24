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

type linter struct {
	fsys     fs.FS
	cfg      *Config
	resolve  Resolver
	catalog  *query.Catalog
	forms    []form
	findings []Finding
}

type form struct {
	name string
	from string
	re   *regexp.Regexp
}

// Lint walks fsys under cfg and returns every finding, in walk order. A
// nil cfg is the defaults. resolve turns a module path into its root
// filesystem; nil refuses every module path. A source or engine that does
// not resolve is a finding against File, as is an extension that does
// not resolve or exports neither native forms nor a reason; the rest of
// the tree is linted with what did.
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
	l.resolveExtensions()
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

func (l *linter) resolveSources() {
	var sources []query.Source
	names := make([]string, 0, len(l.cfg.Sources))
	for ns := range l.cfg.Sources {
		names = append(names, ns)
	}
	sort.Strings(names)
	for _, ns := range names {
		s := l.cfg.Sources[ns]
		src, ok := l.source(ns, s.Path)
		if !ok {
			continue
		}
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

func (l *linter) source(ns, value string) (query.Source, bool) {
	if ns == query.Namespace {
		return query.Patterns(), true
	}
	fsys, base, err := l.locate(value)
	if err != nil {
		l.report(File, 0, fmt.Sprintf("sources.%s: %v", ns, err))
		return query.Source{}, false
	}
	dir := base
	if isProducer(fsys, base) {
		export, err := readExport(fsys, base)
		if err != nil {
			l.report(File, 0, fmt.Sprintf("sources.%s: %v", ns, err))
			return query.Source{}, false
		}
		if export.Patterns == "" {
			l.report(File, 0, fmt.Sprintf("sources.%s: %s exports no patterns", ns, value))
			return query.Source{}, false
		}
		dir = path.Join(base, export.Patterns)
	}
	return query.Publish(ns, fsys, dir), true
}

func (l *linter) resolveEngine() {
	if l.cfg.Engine == "" {
		return
	}
	if export, ok := l.producer("engine: ", l.cfg.Engine); ok {
		l.addForms("engine: ", "", export.NativeForms)
	}
}

func (l *linter) resolveExtensions() {
	for _, ext := range l.cfg.Extensions {
		prefix := "extensions: " + ext + ": "
		export, ok := l.producer(prefix, ext)
		if !ok {
			continue
		}
		if export.NativeForms == nil || (len(export.NativeForms) == 0 && export.Reason == "") {
			l.report(File, 0, fmt.Sprintf("extensions: %s exports neither native forms nor a reason", ext))
			continue
		}
		l.addForms(prefix, ext, export.NativeForms)
	}
}

func (l *linter) producer(prefix, value string) (Export, bool) {
	fsys, base, err := l.locate(value)
	if err != nil {
		l.report(File, 0, prefix+err.Error())
		return Export{}, false
	}
	export, err := readExport(fsys, base)
	if err != nil {
		l.report(File, 0, prefix+err.Error())
		return Export{}, false
	}
	return export, true
}

func (l *linter) addForms(prefix, from string, forms map[string]string) {
	names := make([]string, 0, len(forms))
	for name := range forms {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		re, err := regexp.Compile(forms[name])
		if err != nil {
			l.report(File, 0, fmt.Sprintf("%snative_forms.%s: %v", prefix, name, err))
			continue
		}
		l.forms = append(l.forms, form{name: name, from: from, re: re})
	}
}
