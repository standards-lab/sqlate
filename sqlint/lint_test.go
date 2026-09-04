package sqlint_test

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate/sqlint"
)

// lint loads the configuration from fsys and lints under it, returning
// the findings rendered the way the command prints them, after the lines
// of any configuration error.
func lint(fsys fs.FS, resolve sqlint.Resolver) []string {
	var lines []string
	cfg, err := sqlint.Load(fsys)
	if err != nil {
		lines = append(lines, strings.Split(err.Error(), "\n")...)
	}
	for _, f := range sqlint.Lint(fsys, cfg, resolve) {
		lines = append(lines, f.String())
	}
	return lines
}

// tree is a module the way the lint sees it: a configuration naming the
// library and the engine as directories of the tree, their exports, and
// the files each convention catches.
func tree(config string) fstest.MapFS {
	return fstest.MapFS{
		"sqlint.toml":                          {Data: []byte(config)},
		"lib/sqlint.toml":                      {Data: []byte("[export]\npatterns = \"patterns\"\n")},
		"lib/patterns/guard_where.sql":         {Data: []byte("--| tier: standard\nid = {{id}} AND version = {{version}}")},
		"engine/sqlint.toml":                   {Data: []byte("[export.native_forms]\nreturning = '(?i)\\bRETURNING\\b'\ncast = '::'\n")},
		"domain/a/statements/edit.sql":         {Data: []byte("--| tier: standard\nUPDATE t SET a = {{a}} WHERE {{> sql.guard_where}}")},
		"domain/a/statements/insert_thing.sql": {Data: []byte("--| tier: standard\nINSERT INTO t VALUES ({{x}})")},
		"domain/a/statements/list.sql":         {Data: []byte("--| tier: standard\n-- header prose may mention {{x}}\nSELECT 1 -- but body comments may not: {{docs}}\n, '{{x}}', id::text FROM t RETURNING id")},
		"domain/a/statements/native.sql":       {Data: []byte("--| tier: native\n--| native: postgres — RETURNING\nINSERT INTO t VALUES (1) RETURNING id")},
		"domain/b/statements/broken.sql":       {Data: []byte("--| teir: standard\nSELECT 1")},
		"domain/c/statements/orphan.sql":       {Data: []byte("--| tier: standard\nSELECT 1 WHERE {{> org.nope}}")},
		"admin/x/statements/seed.sql":          {Data: []byte("--| tier: standard\nINSERT INTO t VALUES (1) RETURNING id")},
		"admin/x/patterns/identity.sql":        {Data: []byte("--| tier: native\nRETURNING id, version")},
		"admin/x/migrations/0001_a.up.sql":     {Data: []byte("--| transaction: none\nCREATE INDEX CONCURRENTLY i ON t (x); ANALYZE t")},
		"admin/x/migrations/0002_b.up.sql":     {Data: []byte("--| transaction none\nSELECT 1")},
		"docs/statements/notes.md":             {Data: []byte("ignored")},
	}
}

const config = `
engine = "engine"

[sources]
sql = "lib"

[statements]
dirs = ["domain/*/statements", "admin/*/statements"]

[patterns]
dirs = ["admin/*/patterns"]

[migrations]
dirs = ["admin/*/migrations"]
`

func want(t *testing.T, findings []string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		found := false
		for _, f := range findings {
			found = found || strings.Contains(f, w)
		}
		if !found {
			t.Errorf("missing finding %q in:\n%s", w, strings.Join(findings, "\n"))
		}
	}
}

func reject(t *testing.T, findings []string, rejects ...string) {
	t.Helper()
	for _, f := range findings {
		for _, r := range rejects {
			if strings.Contains(f, r) {
				t.Errorf("unwanted finding: %s", f)
			}
		}
	}
}

func TestLint_FindsEachConvention(t *testing.T) {
	findings := lint(tree(config), nil)
	want(t, findings,
		"insert_thing.sql: named for its SQL verb",
		"list.sql:4: {{ inside a string literal",
		`list.sql:4: "::" (cast) in a standard-tier file`,
		`list.sql:4: "RETURNING" (returning) in a standard-tier file`,
		"list.sql:3: {{ inside a comment",
		"domain/b/statements: query: broken.sql: line 1: unknown declaration",
		`orphan.sql: include of unknown namespace "org" (registered: sql)`,
		`seed.sql:2: "RETURNING" (returning) in a standard-tier file`,
		"admin/x/patterns: query: pattern identity.sql (lint): a native pattern declares",
		"0001_a.up.sql: a non-transactional migration contains exactly one statement",
		"0002_b.up.sql: header: line 1",
	)
	reject(t, findings, "native.sql", "edit.sql", "sqlint.toml")
}

// An override refines the role's switches for one directory set; the
// other sets keep the role's.
func TestLint_OverrideRefinesOneDirectorySet(t *testing.T) {
	findings := lint(tree(config+"\n[statements.\"admin/*/statements\"]\nnative_forms = false\nverb_named = false\n"), nil)
	reject(t, findings, "seed.sql")
	want(t, findings, `list.sql:4: "RETURNING" (returning) in a standard-tier file`, "insert_thing.sql: named for its SQL verb")
}

// A switch off at the role turns the check off everywhere.
func TestLint_RoleSwitchesOff(t *testing.T) {
	cfg := strings.NewReplacer("[statements]\n", "[statements]\nnative_forms = false\n", "[migrations]\n", "[migrations]\nsingle_statement = false\n").Replace(config)
	findings := lint(tree(cfg), nil)
	reject(t, findings, "in a standard-tier file", "contains exactly one statement")
	want(t, findings, "0002_b.up.sql: header")
}

// Without a configuration the roles are every directory of the role's
// name, every check on, and no source: an include is a load error and
// the native-forms list is empty.
func TestLint_Defaults(t *testing.T) {
	fsys := tree(config)
	delete(fsys, "sqlint.toml")
	findings := lint(fsys, nil)
	want(t, findings,
		"insert_thing.sql: named for its SQL verb",
		"list.sql:4: {{ inside a string literal",
		`edit.sql: include of unknown namespace "sql" (registered: )`,
		"0001_a.up.sql: a non-transactional migration contains exactly one statement",
	)
	reject(t, findings, "in a standard-tier file", "lib/patterns", "sqlint.toml")
}

// Errors in the file's own syntax are Load's, each line beginning with
// the file's name, beside the configuration that parsed.
func TestLoad_ReportsConfigurationErrors(t *testing.T) {
	cases := map[string]string{
		`statements: override "nope/*" names no entry of dirs`:                       config + "\n[statements.\"nope/*\"]\nnative_forms = false\n",
		`statements: override "domain/*/statements": "dirs" is not a switch`:         config + "\n[statements.\"domain/*/statements\"]\ndirs = [\"x\"]\n",
		`statements: "single_statement" is neither a switch of the role (verb_named`: strings.Replace(config, "[statements]\n", "[statements]\nsingle_statement = true\n", 1),
		"unknown key checks.x": config + "\n[checks]\nx = true\n",
		"sources.app: a path, or a table with path and overlay": source(`app = 3`),
	}
	for wantMsg, cfg := range cases {
		c, err := sqlint.Load(tree(cfg))
		if err == nil || !strings.Contains(err.Error(), "sqlint.toml: "+wantMsg) {
			t.Errorf("Load error = %v, want %q", err, wantMsg)
		}
		if c == nil || c.Engine != "engine" {
			t.Errorf("Load returned no configuration beside the error")
		}
	}
	if _, err := sqlint.Load(tree("engine = [")); err == nil || !strings.HasPrefix(err.Error(), "sqlint.toml: ") {
		t.Errorf("malformed TOML = %v", err)
	}
}

// A source or engine that does not resolve is a finding against the
// configuration file, and the rest of the tree is linted with what did.
func TestLint_ReportsResolutionFailures(t *testing.T) {
	cases := map[string]string{
		"sources.app: open missing":                      source(`app = "missing"`),
		"sources.app: noexport exports no patterns":      source(`app = "noexport"`),
		"engine: native_forms.bad: error parsing regexp": strings.Replace(config, `engine = "engine"`, `engine = "badengine"`, 1),
		"engine: open gone":                              strings.Replace(config, `engine = "engine"`, `engine = "gone"`, 1),
	}
	for wantMsg, cfg := range cases {
		fsys := tree(cfg)
		fsys["badengine/sqlint.toml"] = &fstest.MapFile{Data: []byte("[export.native_forms]\nbad = '(unclosed'\n")}
		fsys["noexport/sqlint.toml"] = &fstest.MapFile{Data: []byte("[patterns]\ndirs = [\"p\"]\n")}
		c, err := sqlint.Load(fsys)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		findings := sqlint.Lint(fsys, c, nil)
		found := false
		for _, f := range findings {
			found = found || (f.Path == sqlint.File && f.Line == 0 && strings.Contains(f.Message, wantMsg))
		}
		if !found {
			t.Errorf("missing finding %q against %s in:\n%v", wantMsg, sqlint.File, findings)
		}
		want(t, lint(fsys, nil), "insert_thing.sql: named for its SQL verb")
	}
}

// A nil configuration is the defaults, and a finding renders as the
// command prints it.
func TestLint_NilConfigIsDefaultsAndFindingString(t *testing.T) {
	fsys := tree(config)
	delete(fsys, "sqlint.toml")
	findings := sqlint.Lint(fsys, nil, nil)
	if len(findings) == 0 {
		t.Fatal("no findings under the defaults")
	}
	f := sqlint.Finding{Path: "a/b.sql", Line: 3, Message: "m"}
	if f.String() != "a/b.sql:3: m" || (sqlint.Finding{Path: "a/b.sql", Message: "m"}).String() != "a/b.sql: m" {
		t.Errorf("String = %q", f.String())
	}
}

// A module path resolves through the resolver to the module's root
// filesystem, and its export is read there; an overlay resolves the same
// way and respells the library's pattern.
func TestLint_ResolvesPackagePaths(t *testing.T) {
	packages := map[string]fstest.MapFS{
		"github.com/standards-lab/sqlate": {
			"sqlint.toml":              {Data: []byte("[export]\npatterns = \"patterns\"\n")},
			"patterns/paging.sql":      {Data: []byte("--| tier: standard\n OFFSET {{offset}} ROWS FETCH NEXT {{fetch}} ROWS ONLY")},
			"patterns/guard_where.sql": {Data: []byte("--| tier: standard\nid = {{id}} AND version = {{version}}")},
		},
		"github.com/standards-lab/sqlate/mysql": {
			"sqlint.toml":         {Data: []byte("[export]\noverlay = \"patterns\"\n[export.native_forms]\nlimit = '(?i)\\bLIMIT\\b'\n")},
			"patterns/paging.sql": {Data: []byte("--| tier: native\n--| native: mysql — LIMIT\n LIMIT {{fetch}} OFFSET {{offset}}")},
		},
	}
	resolve := sqlint.Resolver(func(module string) (fs.FS, error) {
		if p, ok := packages[module]; ok {
			return p, nil
		}
		return nil, fmt.Errorf("no module %s", module)
	})
	fsys := fstest.MapFS{
		"sqlint.toml": {Data: []byte(`
engine = "github.com/standards-lab/sqlate/mysql"
[sources]
lib = { path = "github.com/standards-lab/sqlate", overlay = "github.com/standards-lab/sqlate/mysql" }
[statements]
dirs = ["domain/*/statements"]
`)},
		"domain/a/statements/edit.sql": {Data: []byte("--| tier: standard\nUPDATE t SET a = {{a}} WHERE {{> lib.guard_where}}")},
		"domain/a/statements/top.sql":  {Data: []byte("--| tier: standard\nSELECT 1 FROM t LIMIT 1")},
	}
	findings := lint(fsys, resolve)
	want(t, findings, `top.sql:2: "LIMIT" (limit) in a standard-tier file`)
	reject(t, findings, "edit.sql", "sqlint.toml")

	fsys["sqlint.toml"] = &fstest.MapFile{Data: []byte("[sources]\nlib = \"github.com/standards-lab/sqlate\"\n[statements]\ndirs = [\"domain/*/statements\"]\n")}
	want(t, lint(fsys, nil), "sqlint.toml: sources.lib: no module resolution for github.com/standards-lab/sqlate")
}

// A bare directory under [sources] is the pattern files themselves, the
// service's own namespace declared entirely in the root file, while a
// directory that contains sqlint.toml is a producer whose export is read.
func TestLint_BareDirectoryIsThePatternsThemselves(t *testing.T) {
	fsys := tree(source(`app = "admin/x/patterns"`))
	fsys["admin/x/patterns/identity.sql"] = &fstest.MapFile{Data: []byte("--| tier: native\n--| native: postgres — RETURNING\nRETURNING id, version")}
	fsys["domain/a/statements/create.sql"] = &fstest.MapFile{Data: []byte("--| tier: native\n--| native: postgres — RETURNING\nINSERT INTO t (a) VALUES ({{a}}) {{> app.identity}}")}
	findings := lint(fsys, nil)
	reject(t, findings, "create.sql", "sqlint.toml", "admin/x/patterns")
	want(t, findings, `orphan.sql: include of unknown namespace "org" (registered: app, sql)`)
}

func source(line string) string {
	return strings.Replace(config, "sql = \"lib\"", "sql = \"lib\"\n"+line, 1)
}

// Every native form the PostgreSQL engine declares trips exactly once on
// the line written for it, and none trips on identifiers, literals,
// comments, or a native-tier file. The engine's real declaration is the
// input, so an entry added there is exercised here.
func TestNativeForms_TrippedOnceAndSilentOtherwise(t *testing.T) {
	engine, err := os.ReadFile("../postgres/sqlint.toml")
	if err != nil {
		t.Fatal(err)
	}
	tripped := map[string]string{
		"returning":    "INSERT INTO t (a) VALUES (1) returning id",
		"on_conflict":  "INSERT INTO t (a) VALUES (1) ON  CONFLICT DO NOTHING",
		"ilike":        "SELECT 1 FROM t WHERE name ILIKE 'x'",
		"concurrently": "CREATE INDEX CONCURRENTLY i ON t (a)",
		"limit":        "SELECT 1 FROM t LIMIT 1",
		"serial":       "CREATE TABLE s (id BIGSERIAL)",
		"jsonb":        "SELECT CAST(a AS jsonb) FROM t",
		"timestamptz":  "SELECT CAST(a AS timestamptz) FROM t",
		"cast":         "SELECT a::text FROM t",
		"pg_catalog":   "SELECT pg_advisory_lock(1)",
		"now":          "SELECT NOW()",
		"uuidv7":       "SELECT uuidv7()",
	}
	silent := []string{
		"SELECT serial_no, rate_limit, returning_at, jsonb_col FROM t",
		"SELECT 1 FROM t WHERE note = 'RETURNING pg_x a::b now() LIMIT'",
		"SELECT 1 FROM t WHERE note = 'it''s' AND a = 1 -- RETURNING :: now()",
		"-- a comment line: RETURNING, LIMIT, ON CONFLICT",
		"SELECT timestamp_tz, returning2 FROM t",
		`SELECT "returning", "pg_x", "ILIKE" FROM t`,
		"SELECT 1 /* RETURNING now() */ FROM t",
		"/* a block comment",
		"   RETURNING, LIMIT, ON CONFLICT",
		"*/ SELECT 1 FROM t",
	}
	fsys := fstest.MapFS{
		"sqlint.toml":        {Data: []byte("engine = \"engine\"\n[statements]\ndirs = [\"s\"]\n")},
		"engine/sqlint.toml": {Data: engine},
		"s/silent.sql":       {Data: []byte("--| tier: standard\n" + strings.Join(silent, "\n"))},
		"s/native.sql":       {Data: []byte("--| tier: native\n--| native: postgres — everything\n" + strings.Join(slices.Sorted(maps.Values(tripped)), "\n"))},
	}
	for name, line := range tripped {
		fsys["s/"+name+".sql"] = &fstest.MapFile{Data: []byte("--| tier: standard\n" + line)}
	}
	findings := lint(fsys, nil)
	reject(t, findings, "silent.sql", "native.sql", "sqlint.toml")
	for name := range tripped {
		n := 0
		for _, f := range findings {
			if strings.Contains(f, name+".sql:2: ") && strings.Contains(f, "("+name+")") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s tripped %d times in:\n%s", name, n, strings.Join(findings, "\n"))
		}
	}
	if len(findings) != len(tripped) {
		t.Errorf("%d findings for %d forms:\n%s", len(findings), len(tripped), strings.Join(findings, "\n"))
	}
}
