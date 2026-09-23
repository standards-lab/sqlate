package query

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
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
//   - declares returning and is not a standard-tier INSERT INTO or UPDATE,
//     or also declares a key or field
//   - declares returning and names a read that is missing, declares
//     returning, a key, a field, or a required transaction, takes a
//     parameter the command does not, or is not SELECT <column>, … FROM …
//     with every column under one qualifier or none, each named once
//   - declares returning, and the dialect's single-statement form of it
//     introduces a {{
func (c *Catalog) Compile(fsys fs.FS, dir string, d sqlate.Dialect) (*Statements, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("query: read %s: %w", dir, err)
	}
	stmts := &Statements{statements: map[string]Statement{}}
	sources := map[string]source{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		text, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("query: read %s: %w", e.Name(), err)
		}
		st, src, err := c.parse(strings.TrimSuffix(e.Name(), ".sql"), string(text), d)
		if err != nil {
			return nil, fmt.Errorf("query: %s: %w", e.Name(), err)
		}
		stmts.statements[st.name] = st
		sources[st.name] = src
	}
	// A returning declaration names a read of the same directory, so it
	// resolves once every file is parsed; in name order, so the first
	// failure reported is stable.
	for _, st := range stmts.Statements() {
		if sources[st.name].returning == "" {
			continue
		}
		if err := resolveReturning(&st, sources, stmts.statements, d); err != nil {
			return nil, fmt.Errorf("query: %s.sql: %w", st.name, err)
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

// Verify prepares every statement against db, and a returning command's
// single-statement form beside it, so a reference the schema no longer
// satisfies fails here rather than at first request. Every failure is
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
		if text := st.ReturningText(); text != "" {
			stmt, err := db.PrepareContext(ctx, text)
			if err != nil {
				errs = append(errs, fmt.Errorf("query: %s (returning): %w", st.name, err))
				continue
			}
			_ = stmt.Close()
		}
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

// source is what Compile keeps of a statement for resolving returning
// declarations: its body after include expansion, its parameters still
// {{…}} slots, and the read its returning declaration names, empty for none.
type source struct {
	body      string
	returning string
}

// parse reads the header, expands the body's pattern includes, and rewrites
// its parameters; the engine receives the body, less a trailing semicolon
// so the statement composes as a derived table. The header grammar: tier
// required (standard | native); native required when the tier is native,
// the engine feature used and the port as free text; port optional and
// native only, the port as its own declaration; transaction optional
// (required); key optional, naming one declared field or several separated
// by commas, each named once; field repeated, "<name> <type>" with an
// optional trailing "not null", matched case-insensitively, the name an
// identifier; returning optional, naming the statement of the same
// directory that reads the changed row back, resolved by Compile once every
// file is parsed. The source returned beside the statement is its expanded
// body and returning declaration, which that resolution reads.
func (c *Catalog) parse(name, text string, d sqlate.Dialect) (Statement, source, error) {
	st := Statement{name: name, dialect: d, catalog: c}
	var src source
	h, err := header.Parse(text)
	if err != nil {
		return st, src, err
	}
	for _, dir := range h.Declarations() {
		switch dir.Key {
		case "tier", "native", "port", "transaction", "key", "field", "returning":
		default:
			return st, src, fmt.Errorf("line %d: unknown declaration %q", dir.Line, dir.Key)
		}
	}
	tier, ok := h.Get("tier")
	if !ok {
		return st, src, errors.New("no tier declaration")
	}
	switch Tier(tier) {
	case TierStandard, TierNative:
		st.tier = Tier(tier)
	default:
		return st, src, fmt.Errorf("tier %q is not standard or native", tier)
	}
	st.native, _ = h.Get("native")
	if st.tier == TierNative && st.native == "" {
		return st, src, errors.New("a native statement declares the engine feature it uses and the port in a native declaration")
	}
	if st.tier == TierStandard && st.native != "" {
		return st, src, errors.New("a standard statement has no native declaration")
	}
	st.port, _ = h.Get("port")
	if st.tier == TierStandard && st.port != "" {
		return st, src, errors.New("a standard statement has no port declaration")
	}
	if tx, ok := h.Get("transaction"); ok {
		if tx != "required" {
			return st, src, fmt.Errorf("transaction declaration %q is not required", tx)
		}
		st.txRequired = true
	}
	for _, f := range h.All("field") {
		decl, notNull := cutNotNull(f)
		fname, typ, ok := strings.Cut(decl, " ")
		typ = strings.TrimSpace(typ)
		if !ok || !identifier.MatchString(fname) || !sqlType.MatchString(typ) {
			return st, src, fmt.Errorf("field declaration %q is not \"<name> <type>\"", f)
		}
		if strayNotNull(typ) {
			return st, src, fmt.Errorf("field declaration %q: the type contains \"not\" or \"null\"; the suffix is %q", f, notNullSuffix)
		}
		st.fields = append(st.fields, Field{Name: fname, Type: typ, NotNull: notNull})
	}
	if key, ok := h.Get("key"); ok {
		for name := range strings.SplitSeq(key, ",") {
			name = strings.TrimSpace(name)
			found := false
			for _, f := range st.fields {
				found = found || f.Name == name
			}
			if !found {
				return st, src, fmt.Errorf("key %q is not a declared field", name)
			}
			if slices.Contains(st.key, name) {
				return st, src, fmt.Errorf("key %q is repeated", name)
			}
			st.key = append(st.key, name)
		}
	}
	src.returning, _ = h.Get("returning")
	if src.returning != "" && !identifier.MatchString(src.returning) {
		return st, src, fmt.Errorf("returning declaration %q is not a statement name", src.returning)
	}
	body, err := c.expand(strings.TrimRight(strings.TrimSpace(text[h.End():]), ";"), st.tier)
	if err != nil {
		return st, src, err
	}
	src.body = body
	st.compiled, err = rewrite(body, d.Placeholder)
	if st.compiled.template != nil {
		st.renderings = &sync.Map{}
	}
	return st, src, err
}

// notNullSuffix is the optional trailing suffix of a field declaration.
const notNullSuffix = " not null"

// cutNotNull returns decl without its trailing "not null" suffix, matched
// case-insensitively as SQL keywords are, and whether the suffix was there.
func cutNotNull(decl string) (string, bool) {
	if len(decl) > len(notNullSuffix) && strings.EqualFold(decl[len(decl)-len(notNullSuffix):], notNullSuffix) {
		return decl[:len(decl)-len(notNullSuffix)], true
	}
	return decl, false
}

// strayNotNull reports whether a field's type, its "not null" suffix cut,
// still contains the word not or null: a misspelled suffix, such as
// "nott null" or "not  null", which the type grammar would otherwise take
// as part of the type, leaving the field nullable and failing only at the
// engine. No SQL type name contains either word.
func strayNotNull(typ string) bool {
	for _, w := range strings.Fields(typ) {
		if strings.EqualFold(w, "not") || strings.EqualFold(w, "null") {
			return true
		}
	}
	return false
}

// identifier is a contract field name as it appears in the composed SQL:
// unquoted, lowercase, the base's own alias for the column.
var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
