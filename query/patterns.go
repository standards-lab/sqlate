package query

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/standards-lab/sqlate/header"
)

// The pattern catalog: reusable protocol SQL, authored as files, each
// declaring its tier, published under a namespace. A pattern's body contains
// slots in the {{ }} syntax; a pattern contains slots only and never includes
// another, so it reads on its own. Two uses:
//
//   - at request time, the collection read composes count, page, and one
//     from the library's patterns over a program's base and the request's
//     declarations; the library fills the slots with text it composed from
//     other patterns, never with request input;
//   - at load time, a statement includes a pattern with {{> namespace.name}},
//     and the pattern's text is spliced in before parameters are rewritten,
//     so a protocol's predicate and columns are written once.
//
// Any package may publish patterns: the library's own are Patterns(), an
// application registers its namespace beside them, and an engine overlays
// the library's request-time patterns it must spell differently. The
// catalog is built once, where the program starts, and every set of
// statements is compiled against it.

//go:embed patterns/*.sql
var patternFiles embed.FS

var (
	slot      = regexp.MustCompile(`\{\{\s*([a-z_][a-z0-9_]*)\s*\}\}`)
	include   = regexp.MustCompile(`\{\{>\s*([a-z_][a-z0-9_]*)\.([a-z_][a-z0-9_]*)\s*\}\}`)
	bare      = regexp.MustCompile(`\{\{>[^}]*\}\}`)
	namespace = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

// Namespace is the library's own namespace: every include is qualified, as
// {{> sql.guard_where}}, so a pattern's origin is visible in the statement
// and two sources cannot collide. A registrant aliases it with As, the way
// an import is aliased, as the last resort against a collision.
const Namespace = "sql"

// Pattern is one catalog entry as the inventory reports it: its namespace,
// name, tier, native note, slots in body order, and body.
type Pattern struct {
	Namespace string
	Name      string
	Tier      Tier
	Native    string
	Slots     []string
	Text      string
}

// layer is one directory of pattern files.
type layer struct {
	fsys fs.FS
	dir  string
}

// Source is one namespace's patterns, declared as a directory and
// any overlays, read and validated when a Catalog is built. It is a value:
// As and Overlay return a new source and leave the receiver as it was.
type Source struct {
	namespace string
	builtin   bool
	base      layer
	overlays  []layer
}

// Publish declares the .sql files under dir in fsys as the patterns of
// namespace: a pattern source. Nothing is read until NewCatalog.
func Publish(namespace string, fsys fs.FS, dir string) Source {
	return Source{namespace: namespace, base: layer{fsys, dir}}
}

// Patterns is the library's own patterns under Namespace: the collection
// read's request-time patterns and the protocol patterns a statement
// includes. A catalog that serves a Projection includes it, under Namespace or
// an alias.
func Patterns() Source {
	s := Publish(Namespace, patternFiles, "patterns")
	s.builtin = true
	return s
}

// Namespace is the namespace the source registers under.
func (s Source) Namespace() string { return s.namespace }

// As registers the source under another namespace, so an include reads
// {{> ns.name}} for it; the last resort when two sources would collide.
func (s Source) As(namespace string) Source {
	s.namespace = namespace
	return s
}

// Overlay replaces patterns of the source by name with the files under dir
// in fsys: an engine supplies its own paging. The replacement is explicit (a
// file that names no pattern of the source, or declares different slots,
// is a catalog error), so an overlay can only respell what the source
// already defines. A later overlay wins over an earlier one.
func (s Source) Overlay(fsys fs.FS, dir string) Source {
	overlays := make([]layer, 0, len(s.overlays)+1)
	overlays = append(overlays, s.overlays...)
	s.overlays = append(overlays, layer{fsys, dir})
	return s
}

// pattern is one catalog entry: its body, tier, and the slots it declares.
type pattern struct {
	name   string
	tier   Tier
	native string
	text   string
	slots  []string
}

// Catalog is the registered pattern sources, read and validated once, and
// the context every statement compiles against. It is read-only after
// NewCatalog and safe for concurrent use.
type Catalog struct {
	namespaces map[string]map[string]pattern
	builtin    string
}

// NewCatalog reads every source and validates it:
//
//   - each file declares a tier
//   - a native file names its port
//   - a pattern includes no other pattern
//   - an overlay respells only what its source defines with the same slots
//   - no two sources share a namespace
//
// Every failure is reported, joined, each naming the namespace and file.
func NewCatalog(sources ...Source) (*Catalog, error) {
	c := &Catalog{namespaces: map[string]map[string]pattern{}}
	var errs []error
	for _, s := range sources {
		if !namespace.MatchString(s.namespace) {
			errs = append(errs, fmt.Errorf("query: namespace %q is not an identifier", s.namespace))
			continue
		}
		if _, dup := c.namespaces[s.namespace]; dup {
			errs = append(errs, fmt.Errorf("query: namespace %q is registered twice; alias one source with As", s.namespace))
			continue
		}
		set, err := readLayer(s.namespace, s.base)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, o := range s.overlays {
			over, err := readLayer(s.namespace, o)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for name, p := range over {
				b, ok := set[name]
				if !ok {
					errs = append(errs, fmt.Errorf("query: overlay %s: %s.sql replaces no pattern of %q", o.dir, name, s.namespace))
					continue
				}
				if !sameSet(b.slots, p.slots) {
					errs = append(errs, fmt.Errorf("query: overlay %s: %s.sql declares slots %v; %s.%s has %v", o.dir, name, p.slots, s.namespace, name, b.slots))
					continue
				}
				set[name] = p
			}
		}
		c.namespaces[s.namespace] = set
		if s.builtin {
			c.builtin = s.namespace
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

// MustCatalog is NewCatalog for the place a program starts, where a catalog
// error is a defect.
func MustCatalog(sources ...Source) *Catalog {
	c, err := NewCatalog(sources...)
	if err != nil {
		panic(err)
	}
	return c
}

// Namespaces returns the registered namespaces in name order.
func (c *Catalog) Namespaces() []string {
	out := make([]string, 0, len(c.namespaces))
	for ns := range c.namespaces {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

// Patterns returns the inventory in namespace, then name, order.
func (c *Catalog) Patterns() []Pattern {
	var out []Pattern
	for ns, set := range c.namespaces {
		for _, p := range set {
			out = append(out, Pattern{Namespace: ns, Name: p.name, Tier: p.tier, Native: p.native, Slots: append([]string(nil), p.slots...), Text: p.text})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// readLayer reads every pattern file of one directory: the header must
// declare a tier, a native tier its port, and the body's {{ }} occurrences
// are its slots; an include inside a pattern is an error.
func readLayer(ns string, l layer) (map[string]pattern, error) {
	entries, err := fs.ReadDir(l.fsys, l.dir)
	if err != nil {
		return nil, fmt.Errorf("query: patterns %s: %w", ns, err)
	}
	out := map[string]pattern{}
	var errs []error
	fail := func(name string, err error) {
		errs = append(errs, fmt.Errorf("query: pattern %s (%s): %w", name, ns, err))
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		text, err := fs.ReadFile(l.fsys, path.Join(l.dir, e.Name()))
		if err != nil {
			fail(e.Name(), err)
			continue
		}
		h, err := header.Parse(string(text))
		if err != nil {
			fail(e.Name(), err)
			continue
		}
		p := pattern{name: strings.TrimSuffix(e.Name(), ".sql")}
		tier, ok := h.Get("tier")
		if !ok {
			fail(e.Name(), errors.New("no tier declaration"))
			continue
		}
		switch p.tier = Tier(tier); p.tier {
		case TierStandard, TierNative:
		default:
			fail(e.Name(), fmt.Errorf("tier %q is not standard or native", tier))
			continue
		}
		p.native, _ = h.Get("native")
		if p.tier == TierNative && p.native == "" {
			fail(e.Name(), errors.New("a native pattern declares the engine feature it uses and the port in a native declaration"))
			continue
		}
		if p.tier == TierStandard && p.native != "" {
			fail(e.Name(), errors.New("a standard pattern has no native declaration"))
			continue
		}
		p.text = strings.TrimRight(string(text)[h.End():], "\n")
		if bare.MatchString(p.text) {
			fail(e.Name(), errors.New("a pattern contains slots only and includes no pattern"))
			continue
		}
		for _, m := range slot.FindAllStringSubmatch(p.text, -1) {
			p.slots = append(p.slots, m[1])
		}
		out[p.name] = p
	}
	return out, errors.Join(errs...)
}

// sameSet reports whether two slot lists name the same slots, in any order
// and multiplicity.
func sameSet(a, b []string) bool {
	set := func(xs []string) map[string]bool {
		m := map[string]bool{}
		for _, x := range xs {
			m[x] = true
		}
		return m
	}
	sa, sb := set(a), set(b)
	if len(sa) != len(sb) {
		return false
	}
	for x := range sa {
		if !sb[x] {
			return false
		}
	}
	return true
}

// lookup finds a pattern by qualified name.
func (c *Catalog) lookup(ns, name string) (pattern, bool) {
	set, ok := c.namespaces[ns]
	if !ok {
		return pattern{}, false
	}
	p, ok := set[name]
	return p, ok
}

// render fills one of the library's request-time patterns, resolved under
// the namespace Patterns() registered as. Every slot must be filled and every
// fill must name a slot; a mismatch is a defect in the library or an
// overlay, not a request error, and panics.
func (c *Catalog) render(name string, fill map[string]string) string {
	if c.builtin == "" {
		panic("query: the catalog contains no library source; a projection needs the library's patterns")
	}
	p, ok := c.lookup(c.builtin, name)
	if !ok {
		panic(fmt.Sprintf("query: no pattern %s.%s", c.builtin, name))
	}
	for _, s := range p.slots {
		if _, ok := fill[s]; !ok {
			panic(fmt.Sprintf("query: pattern %s.%s: slot %q not filled", c.builtin, name, s))
		}
	}
	return slot.ReplaceAllStringFunc(p.text, func(m string) string {
		return fill[slot.FindStringSubmatch(m)[1]]
	})
}

// expand splices {{> namespace.name}} includes into a statement body, so a
// statement contains a pattern's text as if authored there. An unqualified
// include, an unknown namespace or pattern, or a native pattern in a
// standard-tier statement is a load error naming it. Patterns contain no
// includes, so one pass expands everything.
func (c *Catalog) expand(body string, tier Tier) (string, error) {
	var err error
	body = bare.ReplaceAllStringFunc(body, func(m string) string {
		if err != nil {
			return m
		}
		parts := include.FindStringSubmatch(m)
		if parts == nil {
			err = fmt.Errorf("include %s must be qualified: {{> namespace.name}}", m)
			return m
		}
		ns, name := parts[1], parts[2]
		if _, ok := c.namespaces[ns]; !ok {
			err = fmt.Errorf("include of unknown namespace %q (registered: %s)", ns, strings.Join(c.Namespaces(), ", "))
			return m
		}
		p, ok := c.lookup(ns, name)
		if !ok {
			err = fmt.Errorf("include of unknown pattern %q", ns+"."+name)
			return m
		}
		if tier == TierStandard && p.tier == TierNative {
			err = fmt.Errorf("include of native pattern %q in a standard-tier statement", ns+"."+name)
			return m
		}
		return p.text
	})
	return body, err
}
