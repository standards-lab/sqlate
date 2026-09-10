package query

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/header"
)

// Statements is a set of related statements compiled from one directory:
// the inventory the verification step and an administrative listing walk,
// keyed by file name. It is not a registry; a program fetches each
// statement once, where it binds them.
type Statements struct {
	statements map[string]Statement
}

// Compile reads every .sql file under dir in fsys, parses its
// header, expands its includes against the catalog, and resolves its
// parameters against d's placeholders. A load error names the file and is a
// defect in the program's own files, not a runtime condition. It's a load
// error when a file:
//
//   - has no header
//   - has an unknown declaration
//   - has a header the grammar rejects
//   - has an include the catalog cannot resolve
func (c *Catalog) Compile(fsys fs.FS, dir string, d sqlate.Dialect) (*Statements, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("query: read %s: %w", dir, err)
	}
	stmts := &Statements{statements: map[string]Statement{}}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		text, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("query: read %s: %w", e.Name(), err)
		}
		st, err := c.parse(strings.TrimSuffix(e.Name(), ".sql"), string(text), d)
		if err != nil {
			return nil, fmt.Errorf("query: %s: %w", e.Name(), err)
		}
		stmts.statements[st.name] = st
	}
	return stmts, nil
}

// MustCompile is Compile for the place a program binds its statements,
// where a load error is a defect.
func (c *Catalog) MustCompile(fsys fs.FS, dir string, d sqlate.Dialect) *Statements {
	stmts, err := c.Compile(fsys, dir, d)
	if err != nil {
		panic(err)
	}
	return stmts
}

// Statement returns the statement named by its file's base name; a missing
// name is a defect in the caller's constructor and panics.
func (s *Statements) Statement(name string) Statement {
	st, ok := s.statements[name]
	if !ok {
		panic(fmt.Sprintf("query: no statement %q", name))
	}
	return st
}

// Statements returns the inventory in name order.
func (s *Statements) Statements() []Statement {
	out := make([]Statement, 0, len(s.statements))
	for _, st := range s.statements {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// Verify prepares every statement against db, so a reference the schema no
// longer satisfies fails here rather than at first request. Every failure is
// reported, joined, each naming its statement.
func (s *Statements) Verify(ctx context.Context, db sqlate.Session) error {
	var errs []error
	for _, st := range s.Statements() {
		stmt, err := db.PrepareContext(ctx, st.compiled.text)
		if err != nil {
			errs = append(errs, fmt.Errorf("query: %s: %w", st.name, err))
			continue
		}
		_ = stmt.Close()
	}
	return errors.Join(errs...)
}

// Verifier is what Verify composes: Statements, a Projection, anything that
// can check itself against the live schema.
type Verifier interface {
	Verify(ctx context.Context, db sqlate.Session) error
}

// Verify runs every verifier and joins their failures; startup and an
// administrative check call it with the same arguments.
func Verify(ctx context.Context, db sqlate.Session, vs ...Verifier) error {
	var errs []error
	for _, v := range vs {
		if err := v.Verify(ctx, db); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// parse reads the header, expands the body's pattern includes, and rewrites
// its parameters; the engine receives the body, less a trailing semicolon
// so the statement composes as a derived table. The header grammar: tier
// required (standard | native); native required when the tier is native,
// the engine feature used and the port as free text; transaction optional
// (required); key optional,
// naming a field; field repeated, "<name> <type>", the name an identifier.
func (c *Catalog) parse(name, text string, d sqlate.Dialect) (Statement, error) {
	st := Statement{name: name, dialect: d, catalog: c}
	h, err := header.Parse(text)
	if err != nil {
		return st, err
	}
	for _, dir := range h.Declarations() {
		switch dir.Key {
		case "tier", "native", "transaction", "key", "field":
		default:
			return st, fmt.Errorf("line %d: unknown declaration %q", dir.Line, dir.Key)
		}
	}
	tier, ok := h.Get("tier")
	if !ok {
		return st, errors.New("no tier declaration")
	}
	switch Tier(tier) {
	case TierStandard, TierNative:
		st.tier = Tier(tier)
	default:
		return st, fmt.Errorf("tier %q is not standard or native", tier)
	}
	st.native, _ = h.Get("native")
	if st.tier == TierNative && st.native == "" {
		return st, errors.New("a native statement declares the engine feature it uses and the port in a native declaration")
	}
	if st.tier == TierStandard && st.native != "" {
		return st, errors.New("a standard statement has no native declaration")
	}
	if tx, ok := h.Get("transaction"); ok {
		if tx != "required" {
			return st, fmt.Errorf("transaction declaration %q is not required", tx)
		}
		st.txRequired = true
	}
	for _, f := range h.All("field") {
		fname, typ, ok := strings.Cut(f, " ")
		typ = strings.TrimSpace(typ)
		if !ok || !identifier.MatchString(fname) || !sqlType.MatchString(typ) {
			return st, fmt.Errorf("field declaration %q is not \"<name> <type>\"", f)
		}
		st.fields = append(st.fields, Field{Name: fname, Type: typ})
	}
	if key, ok := h.Get("key"); ok {
		found := false
		for _, f := range st.fields {
			found = found || f.Name == key
		}
		if !found {
			return st, fmt.Errorf("key %q is not a declared field", key)
		}
		st.key = key
	}
	body, err := c.expand(strings.TrimRight(strings.TrimSpace(text[h.End():]), ";"), st.tier)
	if err != nil {
		return st, err
	}
	st.compiled, err = rewrite(body, d.Placeholder)
	if st.compiled.template != nil {
		st.renderings = &sync.Map{}
	}
	return st, err
}

// identifier is a contract field name as it appears in the composed SQL:
// unquoted, lowercase, the base's own alias for the column.
var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
