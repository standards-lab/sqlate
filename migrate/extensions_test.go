package migrate_test

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	"github.com/standards-lab/sqlate/sqltest"
)

type extensionDialect struct{ lockingDialect }

func (extensionDialect) ExtensionState(param string) string {
	return "SELECT installed, default, path FROM extensions WHERE name = " + param
}

func (extensionDialect) ActivateExtension(name string) []string {
	return []string{`CREATE EXTENSION IF NOT EXISTS "` + name + `"`, `ALTER EXTENSION "` + name + `" UPDATE`}
}

func (extensionDialect) RemoveExtension(name string) string {
	return `DROP EXTENSION IF EXISTS "` + name + `" RESTRICT`
}

var stateCols = []string{"installed", "default", "path"}

func state(installed, def driver.Value, path bool) sqltest.Response {
	return sqltest.Response{Columns: stateCols, Rows: [][]driver.Value{{installed, def, path}}}
}

func newExtensionMigrator(t *testing.T, declared []migrate.Extension, responses ...sqltest.Response) (*migrate.Migrator, *sqltest.Recorder) {
	t.Helper()
	return newExtensionSets(t, []migrate.Set{{Name: "app", Migrations: set}}, migrate.Options{Extensions: declared}, responses...)
}

func newExtensionSets(t *testing.T, sets []migrate.Set, opts migrate.Options, responses ...sqltest.Response) (*migrate.Migrator, *sqltest.Recorder) {
	t.Helper()
	pool, rec := sqltest.Open(t, responses...)
	m, err := migrate.New(sqlate.Wrap(pool, extensionDialect{}), sets, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m, rec
}

func TestNew_ValidatesExtensions(t *testing.T) {
	pool, rec := sqltest.Open(t)
	capable := sqlate.Wrap(pool, extensionDialect{})
	incapable := sqlate.Wrap(pool, lockingDialect{})
	sets := []migrate.Set{{Name: "app", Migrations: set}}
	refused := map[string][]migrate.Extension{
		"empty name":     {{Name: "hstore"}, {Name: ""}},
		"duplicate":      {{Name: "hstore"}, {Name: "citext"}, {Name: "hstore"}},
		"removed reused": {{Name: "hstore"}, {Name: "hstore", Removed: true}},
	}
	for name, declared := range refused {
		if _, err := migrate.New(capable, sets, migrate.Options{Extensions: declared}); err == nil {
			t.Errorf("%s: New accepted the declarations", name)
		}
	}
	if _, err := migrate.New(incapable, sets, migrate.Options{Extensions: []migrate.Extension{{Name: "hstore"}}}); err == nil {
		t.Error("New accepted a declaration on a dialect without the extension capability")
	}
	if _, err := migrate.New(incapable, sets, migrate.Options{}); err != nil {
		t.Errorf("New refused no declarations on a dialect without the capability: %v", err)
	}
	if _, err := migrate.New(capable, sets, migrate.Options{Extensions: []migrate.Extension{{Name: "hstore"}, {Name: "citext", Removed: true}}}); err != nil {
		t.Errorf("New refused distinct declarations: %v", err)
	}
	if calls := rec.Calls(); len(calls) != 0 {
		t.Errorf("New performed I/O: %v", calls)
	}
}

func TestExtensions_ReportsEachDeclarationInOrderOnThePool(t *testing.T) {
	declared := []migrate.Extension{
		{Name: "hstore"},
		{Name: "citext"},
		{Name: "pg_trgm"},
		{Name: "absent"},
		{Name: "newer"},
		{Name: "stranded"},
		{Name: "old", Removed: true},
		{Name: "gone", Removed: true},
		{Name: "orphan", Removed: true},
	}
	m, rec := newExtensionMigrator(t, declared,
		state("1.4", "1.8", true),
		state("1.6", "1.6", false),
		state(nil, "1.6", false),
		state(nil, nil, false),
		state("2.0", "1.8", false),
		state("1.1", "1.8", false),
		state("1.0", "1.0", false),
		state(nil, "1.2", false),
		state("0.9", nil, false),
	)
	got, err := m.Extensions(context.Background())
	if err != nil {
		t.Fatalf("Extensions: %v", err)
	}
	want := []migrate.ExtensionStatus{
		{Name: "hstore", Installed: "1.4", Default: "1.8", Pending: true},
		{Name: "citext", Installed: "1.6", Default: "1.6"},
		{Name: "pg_trgm", Default: "1.6", Pending: true},
		{Name: "absent"},
		{Name: "newer", Installed: "2.0", Default: "1.8"},
		{Name: "stranded", Installed: "1.1", Default: "1.8"},
		{Name: "old", Removed: true, Installed: "1.0", Default: "1.0", Pending: true},
		{Name: "gone", Removed: true, Default: "1.2"},
		{Name: "orphan", Removed: true, Installed: "0.9", Pending: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Extensions =\n%+v\nwant\n%+v", got, want)
	}
	assertOps(t, rec, slices.Repeat([]sqltest.Op{sqltest.OpQuery}, len(declared))...)
	query := extensionDialect{}.ExtensionState("$1")
	for i, c := range rec.Calls() {
		if c.SQL != query || len(c.Args) != 1 || c.Args[0] != declared[i].Name {
			t.Errorf("call %d = %+v, want the state query bound to %q", i, c, declared[i].Name)
		}
	}
}

func TestExtensions_NoRowReadsAsNeitherInstalledNorOffered(t *testing.T) {
	m, rec := newExtensionMigrator(t, []migrate.Extension{{Name: "hstore"}, {Name: "citext", Removed: true}},
		sqltest.Response{Columns: stateCols},
		sqltest.Response{Columns: stateCols},
	)
	got, err := m.Extensions(context.Background())
	if err != nil {
		t.Fatalf("Extensions: %v", err)
	}
	want := []migrate.ExtensionStatus{
		{Name: "hstore"},
		{Name: "citext", Removed: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Extensions = %+v, want %+v", got, want)
	}
	assertOps(t, rec, sqltest.OpQuery, sqltest.OpQuery)
}

func TestExtensions_NoDeclarationsReadsNothing(t *testing.T) {
	m, rec := newExtensionMigrator(t, nil)
	got, err := m.Extensions(context.Background())
	if err != nil || len(got) != 0 {
		t.Errorf("Extensions = %v, %v; want none", got, err)
	}
	assertOps(t, rec)
}

func TestExtensions_StateFailureNamesTheExtension(t *testing.T) {
	m, rec := newExtensionMigrator(t, []migrate.Extension{{Name: "hstore"}, {Name: "citext"}},
		state("1.8", "1.8", false),
		failed,
	)
	got, err := m.Extensions(context.Background())
	if got != nil {
		t.Errorf("Extensions returned %+v alongside its error", got)
	}
	if !errors.Is(err, errDriver) {
		t.Fatalf("Extensions = %v, want the engine error", err)
	}
	if _, ok := errors.AsType[*sqltest.MappedError](err); !ok {
		t.Error("engine error did not cross the mapping boundary")
	}
	if e, ok := errors.AsType[*migrate.ExtensionError](err); !ok || e.Extension != "citext" {
		t.Errorf("Extensions = %v, want an *ExtensionError naming citext", err)
	}
	assertOps(t, rec, sqltest.OpQuery, sqltest.OpQuery)
}

func TestExtensionError_ClassifiesAndReachesTheDetail(t *testing.T) {
	unavailable := error(&migrate.ExtensionError{Extension: "vector", Err: migrate.ErrExtensionUnavailable})
	noPath := error(&migrate.ExtensionError{Extension: "hstore", Err: &migrate.NoUpdatePathError{Installed: "2.0", Default: "1.8"}})
	pending := error(&migrate.ExtensionError{Extension: "citext", Err: &migrate.ExtensionPendingError{Installed: "1.4", Default: "1.6"}})
	classes := []error{migrate.ErrExtensionUnavailable, migrate.ErrNoUpdatePath, migrate.ErrPending}
	for i, err := range []error{unavailable, noPath, pending} {
		for j, class := range classes {
			if got := errors.Is(err, class); got != (i == j) {
				t.Errorf("errors.Is(%v, %v) = %v", err, class, got)
			}
		}
	}
	if e, ok := errors.AsType[*migrate.NoUpdatePathError](noPath); !ok || e.Installed != "2.0" || e.Default != "1.8" {
		t.Errorf("no-path detail = %+v, %v", e, ok)
	}
	if e, ok := errors.AsType[*migrate.ExtensionPendingError](pending); !ok || e.Installed != "1.4" || e.Default != "1.6" {
		t.Errorf("pending detail = %+v, %v", e, ok)
	}
	for _, c := range []struct {
		err  error
		want []string
	}{
		{unavailable, []string{`"vector"`}},
		{noPath, []string{`"hstore"`, "2.0", "1.8"}},
		{pending, []string{`"citext"`, "1.4", "1.6"}},
		{&migrate.ExtensionError{Extension: "pg_trgm", Err: &migrate.ExtensionPendingError{Default: "1.6"}}, []string{`"pg_trgm"`, "not installed", "1.6"}},
		{&migrate.ExtensionError{Extension: "old", Err: &migrate.ExtensionPendingError{Removed: true, Installed: "1.0"}}, []string{`"old"`, "removed", "1.0"}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.err.Error(), w) {
				t.Errorf("%q does not name %s", c.err.Error(), w)
			}
		}
	}
}

var (
	stateQuery = extensionDialect{}.ExtensionState("$1")
	allApplied = history([]driver.Value{int64(1), "a", false}, []driver.Value{int64(2), "b", false})
)

func trace(calls []sqltest.Call) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = string(c.Op) + " " + c.SQL
	}
	return out
}

func activation(name string) []string {
	out := []string{"begin "}
	for _, stmt := range (extensionDialect{}).ActivateExtension(name) {
		out = append(out, "exec "+stmt)
	}
	return append(out, "commit ")
}

func assertNoExtensionStatement(t *testing.T, rec *sqltest.Recorder) {
	t.Helper()
	for _, c := range rec.Calls() {
		if c.SQL == stateQuery || strings.Contains(c.SQL, "EXTENSION") {
			t.Errorf("ran extension statement %q", c.SQL)
		}
	}
}

func TestUp_ActivatesInDeclaredOrderEachInItsOwnTransactionBeforeTheSets(t *testing.T) {
	var logs bytes.Buffer
	pool, rec := sqltest.Open(t,
		locked, created, history(),
		state("1.4", "1.8", true),
		state("1.6", "1.6", false),
		state(nil, "1.6", false),
		ran, ran,
		ran, ran,
		ran, rowWritten,
		rowWritten, ran, rowWritten,
		unlocked,
	)
	m, err := migrate.New(sqlate.Wrap(pool, extensionDialect{}), []migrate.Set{{Name: "app", Migrations: set}}, migrate.Options{
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		Extensions: []migrate.Extension{
			{Name: "hstore"},
			{Name: "citext"},
			{Name: "old", Removed: true},
			{Name: "pg_trgm"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	assertOps(t, rec,
		sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery,
		sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery,
		sqltest.OpBegin, sqltest.OpExec, sqltest.OpExec, sqltest.OpCommit,
		sqltest.OpBegin, sqltest.OpExec, sqltest.OpExec, sqltest.OpCommit,
		sqltest.OpBegin, sqltest.OpExec, sqltest.OpExec, sqltest.OpCommit,
		sqltest.OpExec, sqltest.OpExec, sqltest.OpExec,
		sqltest.OpQuery,
	)
	calls := rec.Calls()
	if len(calls) != 22 {
		t.Fatalf("%d calls, want 22", len(calls))
	}
	for i, name := range []string{"hstore", "citext", "pg_trgm"} {
		if c := calls[3+i]; c.SQL != stateQuery || c.Args[0] != name {
			t.Errorf("state read %d = %+v, want %s", i, c, name)
		}
	}
	want := slices.Concat(activation("hstore"), activation("pg_trgm"))
	if got := trace(calls[6:14]); !slices.Equal(got, want) {
		t.Errorf("activations =\n%q\nwant\n%q", got, want)
	}
	if calls[15].SQL != set[0].Up || calls[19].SQL != set[1].Up {
		t.Errorf("sets ran %q and %q after the activations", calls[15].SQL, calls[19].SQL)
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("log =\n%s\nwant two activations and two applies", logs.String())
	}
	for i, w := range [][]string{
		{"extension activated", "extension=hstore", "version=1.8", "from=1.4"},
		{"extension activated", "extension=pg_trgm", "version=1.6"},
		{"migration applied", "version=1"},
		{"migration applied", "version=2"},
	} {
		for _, part := range w {
			if !strings.Contains(lines[i], part) {
				t.Errorf("log line %d = %q, want %q", i, lines[i], part)
			}
		}
	}
	if strings.Contains(lines[1], "from=") {
		t.Errorf("log line %q names an installed version for a missing extension", lines[1])
	}
}

func TestUp_DeclarationsAtTheDefaultRunNoStatement(t *testing.T) {
	m, rec := newExtensionMigrator(t, []migrate.Extension{{Name: "hstore"}, {Name: "citext"}},
		locked, created, allApplied,
		state("1.8", "1.8", false),
		state("1.6", "1.6", true),
		unlocked,
	)
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	assertOps(t, rec,
		sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery,
		sqltest.OpQuery, sqltest.OpQuery,
		sqltest.OpQuery,
	)
}

func TestApply_ChecksEveryDeclarationBeforeActivatingAnyOrApplyingASet(t *testing.T) {
	declared := []migrate.Extension{{Name: "hstore"}, {Name: "citext"}}
	runs := map[string]func(*migrate.Migrator) error{
		"Up":             func(m *migrate.Migrator) error { return m.Up(context.Background()) },
		"Migrator.Steps": func(m *migrate.Migrator) error { return m.Steps(context.Background(), 1) },
		"Layer.Steps":    func(m *migrate.Migrator) error { return m.Layers()[0].Steps(context.Background(), 1) },
	}
	for run, fn := range runs {
		t.Run(run+"/newer than the default", func(t *testing.T) {
			m, rec := newExtensionMigrator(t, declared,
				locked, created, history(),
				state(nil, "1.8", false),
				state("2.0", "1.8", false),
				unlocked,
			)
			err := fn(m)
			if e, ok := errors.AsType[*migrate.ExtensionError](err); !ok || e.Extension != "citext" {
				t.Fatalf("%s = %v, want an *ExtensionError naming citext", run, err)
			}
			if e, ok := errors.AsType[*migrate.NoUpdatePathError](err); !ok || e.Installed != "2.0" || e.Default != "1.8" {
				t.Errorf("%s = %v, want a *NoUpdatePathError naming 2.0 and 1.8", run, err)
			}
			assertOps(t, rec,
				sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery,
				sqltest.OpQuery, sqltest.OpQuery,
				sqltest.OpQuery,
			)
		})
		t.Run(run+"/behind with no path", func(t *testing.T) {
			m, rec := newExtensionMigrator(t, declared,
				locked, created, history(),
				state("1.4", "1.8", true),
				state("1.1", "1.6", false),
				unlocked,
			)
			if e, ok := errors.AsType[*migrate.NoUpdatePathError](fn(m)); !ok || e.Installed != "1.1" || e.Default != "1.6" {
				t.Errorf("%s = %+v, want a *NoUpdatePathError naming 1.1 and 1.6", run, e)
			}
			assertOps(t, rec,
				sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery,
				sqltest.OpQuery, sqltest.OpQuery,
				sqltest.OpQuery,
			)
		})
		t.Run(run+"/unavailable", func(t *testing.T) {
			m, rec := newExtensionMigrator(t, declared,
				locked, created, history(),
				state(nil, "1.8", false),
				state(nil, nil, false),
				unlocked,
			)
			err := fn(m)
			if !errors.Is(err, migrate.ErrExtensionUnavailable) {
				t.Fatalf("%s = %v, want ErrExtensionUnavailable", run, err)
			}
			if e, ok := errors.AsType[*migrate.ExtensionError](err); !ok || e.Extension != "citext" {
				t.Errorf("%s = %v, want an *ExtensionError naming citext", run, err)
			}
			assertOps(t, rec,
				sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery,
				sqltest.OpQuery, sqltest.OpQuery,
				sqltest.OpQuery,
			)
		})
	}
}

func TestUp_ActivationFailureStopsBeforeTheNextDeclarationAndTheSets(t *testing.T) {
	m, rec := newExtensionMigrator(t, []migrate.Extension{{Name: "hstore"}, {Name: "pg_trgm"}},
		locked, created, history(),
		state(nil, "1.8", false),
		state(nil, "1.6", false),
		failed,
		unlocked,
	)
	err := m.Up(context.Background())
	if !errors.Is(err, errDriver) {
		t.Fatalf("Up = %v, want the engine error", err)
	}
	if _, ok := errors.AsType[*sqltest.MappedError](err); !ok {
		t.Error("engine error did not cross the mapping boundary")
	}
	if e, ok := errors.AsType[*migrate.ExtensionError](err); !ok || e.Extension != "hstore" {
		t.Errorf("Up = %v, want an *ExtensionError naming hstore", err)
	}
	assertOps(t, rec,
		sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery,
		sqltest.OpQuery, sqltest.OpQuery,
		sqltest.OpBegin, sqltest.OpExec, sqltest.OpRollback,
		sqltest.OpQuery,
	)
}

func TestSteps_PositiveActivatesBeforeApplying(t *testing.T) {
	declared := []migrate.Extension{{Name: "pg_trgm"}}
	t.Run("Migrator.Steps", func(t *testing.T) {
		m, rec := newExtensionMigrator(t, declared,
			locked, created, history(),
			state(nil, "1.6", false),
			ran, ran,
			ran, rowWritten,
			unlocked,
		)
		if err := m.Steps(context.Background(), 1); err != nil {
			t.Fatalf("Steps: %v", err)
		}
		calls := rec.Calls()
		assertOps(t, rec,
			sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery,
			sqltest.OpQuery,
			sqltest.OpBegin, sqltest.OpExec, sqltest.OpExec, sqltest.OpCommit,
			sqltest.OpBegin, sqltest.OpExec, sqltest.OpExec, sqltest.OpCommit,
			sqltest.OpQuery,
		)
		if len(calls) == 13 {
			if got := trace(calls[4:8]); !slices.Equal(got, activation("pg_trgm")) {
				t.Errorf("activation = %q", got)
			}
			if calls[9].SQL != set[0].Up {
				t.Errorf("applied %q after the activation, want %q", calls[9].SQL, set[0].Up)
			}
		}
	})
	t.Run("Layer.Steps", func(t *testing.T) {
		m, rec := newExtensionSets(t, []migrate.Set{library, app}, migrate.Options{Extensions: declared},
			locked, created, history(), created, history(),
			state(nil, "1.6", false),
			ran, ran,
			step, rowWritten,
			unlocked,
		)
		lib, _ := m.Layer("lib")
		if err := lib.Steps(context.Background(), 1); err != nil {
			t.Fatalf("Steps: %v", err)
		}
		calls := rec.Calls()
		assertOps(t, rec,
			sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery, sqltest.OpExec, sqltest.OpQuery,
			sqltest.OpQuery,
			sqltest.OpBegin, sqltest.OpExec, sqltest.OpExec, sqltest.OpCommit,
			sqltest.OpBegin, sqltest.OpExec, sqltest.OpExec, sqltest.OpCommit,
			sqltest.OpQuery,
		)
		if len(calls) == 15 {
			if got := trace(calls[6:10]); !slices.Equal(got, activation("pg_trgm")) {
				t.Errorf("activation = %q", got)
			}
			if calls[11].SQL != library.Migrations[0].Up {
				t.Errorf("applied %q after the activation, want %q", calls[11].SQL, library.Migrations[0].Up)
			}
		}
	})
	t.Run("refused below pending", func(t *testing.T) {
		m, rec := newExtensionSets(t, []migrate.Set{library, app}, migrate.Options{Extensions: declared},
			locked, created, history(), created, history(),
			unlocked,
		)
		top, _ := m.Layer("app")
		if err := top.Steps(context.Background(), 1); !errors.Is(err, migrate.ErrBelowPending) {
			t.Fatalf("Steps = %v, want ErrBelowPending", err)
		}
		assertOps(t, rec,
			sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery, sqltest.OpExec, sqltest.OpQuery,
			sqltest.OpQuery,
		)
		assertNoExtensionStatement(t, rec)
	})
}

func TestUp_UnlockedRunsTheStageWithoutTheLock(t *testing.T) {
	m, rec := newExtensionSets(t, []migrate.Set{{Name: "app", Migrations: set}}, migrate.Options{
		Unlocked:   true,
		Extensions: []migrate.Extension{{Name: "pg_trgm"}},
	},
		created, allApplied,
		state(nil, "1.6", false),
		ran, ran,
	)
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	assertOps(t, rec,
		sqltest.OpExec, sqltest.OpQuery,
		sqltest.OpQuery,
		sqltest.OpBegin, sqltest.OpExec, sqltest.OpExec, sqltest.OpCommit,
	)
	if got := trace(rec.Calls()); len(got) == 7 && !slices.Equal(got[3:], activation("pg_trgm")) {
		t.Errorf("activation = %q", got[3:])
	}
}

func TestRevertsAndForce_RunNoExtensionStatement(t *testing.T) {
	declared := []migrate.Extension{{Name: "pg_trgm"}}
	revert := []sqltest.Op{
		sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery,
		sqltest.OpExec, sqltest.OpExec, sqltest.OpExec,
		sqltest.OpQuery,
	}
	cases := []struct {
		name      string
		run       func(*migrate.Migrator) error
		responses []sqltest.Response
		ops       []sqltest.Op
	}{
		{
			"Down", func(m *migrate.Migrator) error { return m.Down(context.Background(), 1) },
			[]sqltest.Response{locked, created, allApplied, rowWritten, ran, rowWritten, unlocked},
			revert,
		},
		{
			"Steps negative", func(m *migrate.Migrator) error { return m.Steps(context.Background(), -1) },
			[]sqltest.Response{locked, created, allApplied, rowWritten, ran, rowWritten, unlocked},
			revert,
		},
		{
			"Steps zero", func(m *migrate.Migrator) error { return m.Steps(context.Background(), 0) },
			nil,
			nil,
		},
		{
			"Reset", func(m *migrate.Migrator) error { return m.Reset(context.Background()) },
			[]sqltest.Response{locked, created, allApplied, rowWritten, ran, rowWritten, ran, rowWritten, ran, unlocked},
			[]sqltest.Op{
				sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery,
				sqltest.OpExec, sqltest.OpExec, sqltest.OpExec,
				sqltest.OpBegin, sqltest.OpExec, sqltest.OpExec, sqltest.OpCommit,
				sqltest.OpExec,
				sqltest.OpQuery,
			},
		},
		{
			"Force", func(m *migrate.Migrator) error { return m.Force(context.Background(), 1) },
			[]sqltest.Response{locked, created, rowWritten, rowMissing, rowWritten, unlocked},
			[]sqltest.Op{sqltest.OpExec, sqltest.OpExec, sqltest.OpExec, sqltest.OpExec, sqltest.OpExec, sqltest.OpQuery},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, rec := newExtensionMigrator(t, declared, c.responses...)
			if err := c.run(m); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			assertOps(t, rec, c.ops...)
			assertNoExtensionStatement(t, rec)
		})
	}
}

func TestVerify_ClassifiesEachExtensionFault(t *testing.T) {
	classes := []error{migrate.ErrExtensionUnavailable, migrate.ErrNoUpdatePath, migrate.ErrPending}
	cases := []struct {
		name    string
		decl    migrate.Extension
		state   sqltest.Response
		class   error
		pending *migrate.ExtensionPendingError
		noPath  *migrate.NoUpdatePathError
	}{
		{"missing", migrate.Extension{Name: "pg_trgm"}, state(nil, "1.6", false), migrate.ErrPending, &migrate.ExtensionPendingError{Default: "1.6"}, nil},
		{"behind", migrate.Extension{Name: "pg_trgm"}, state("1.4", "1.8", true), migrate.ErrPending, &migrate.ExtensionPendingError{Installed: "1.4", Default: "1.8"}, nil},
		{"removed installed", migrate.Extension{Name: "old", Removed: true}, state("1.0", "1.0", false), migrate.ErrPending, &migrate.ExtensionPendingError{Removed: true, Installed: "1.0", Default: "1.0"}, nil},
		{"removed installed, not offered", migrate.Extension{Name: "old", Removed: true}, state("0.9", nil, false), migrate.ErrPending, &migrate.ExtensionPendingError{Removed: true, Installed: "0.9"}, nil},
		{"unavailable", migrate.Extension{Name: "vector"}, state(nil, nil, false), migrate.ErrExtensionUnavailable, nil, nil},
		{"installed, not offered", migrate.Extension{Name: "vector"}, state("0.5", nil, false), migrate.ErrExtensionUnavailable, nil, nil},
		{"newer than the default", migrate.Extension{Name: "hstore"}, state("2.0", "1.8", false), migrate.ErrNoUpdatePath, nil, &migrate.NoUpdatePathError{Installed: "2.0", Default: "1.8"}},
		{"behind with no path", migrate.Extension{Name: "hstore"}, state("1.1", "1.8", false), migrate.ErrNoUpdatePath, nil, &migrate.NoUpdatePathError{Installed: "1.1", Default: "1.8"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, rec := newExtensionMigrator(t, []migrate.Extension{{Name: "citext"}, c.decl},
				state("1.6", "1.6", false),
				c.state,
			)
			err := m.Verify(context.Background())
			if e, ok := errors.AsType[*migrate.ExtensionError](err); !ok || e.Extension != c.decl.Name {
				t.Fatalf("Verify = %v, want an *ExtensionError naming %s", err, c.decl.Name)
			}
			if !strings.Contains(err.Error(), `"`+c.decl.Name+`"`) {
				t.Errorf("%q does not name %s", err.Error(), c.decl.Name)
			}
			for _, class := range classes {
				if got := errors.Is(err, class); got != (class == c.class) {
					t.Errorf("errors.Is(%v, %v) = %v", err, class, got)
				}
			}
			e, ok := errors.AsType[*migrate.ExtensionPendingError](err)
			if ok != (c.pending != nil) || ok && *e != *c.pending {
				t.Errorf("pending detail = %+v, %v; want %+v", e, ok, c.pending)
			}
			n, ok := errors.AsType[*migrate.NoUpdatePathError](err)
			if ok != (c.noPath != nil) || ok && *n != *c.noPath {
				t.Errorf("no-path detail = %+v, %v; want %+v", n, ok, c.noPath)
			}
			if _, ok := errors.AsType[*migrate.SetError](err); ok {
				t.Errorf("Verify = %v, an extension fault inside a *SetError", err)
			}
			assertOps(t, rec, sqltest.OpQuery, sqltest.OpQuery)
		})
	}
}

func TestVerify_ChecksExtensionsInDeclaredOrderOnThePoolThenTheSets(t *testing.T) {
	declared := []migrate.Extension{{Name: "hstore"}, {Name: "gone", Removed: true}, {Name: "citext"}}
	t.Run("passing", func(t *testing.T) {
		m, rec := newExtensionMigrator(t, declared,
			state("1.8", "1.8", false),
			state(nil, "1.2", false),
			state("1.6", "1.6", true),
			exists(true), allApplied,
		)
		if err := m.Verify(context.Background()); err != nil {
			t.Fatalf("Verify = %v", err)
		}
		assertOps(t, rec, slices.Repeat([]sqltest.Op{sqltest.OpQuery}, 5)...)
		calls := rec.Calls()
		for i, e := range declared {
			if i < len(calls) && (calls[i].SQL != stateQuery || calls[i].Args[0] != e.Name) {
				t.Errorf("call %d = %+v, want the state query bound to %q", i, calls[i], e.Name)
			}
		}
		assertNoLock(t, rec)
	})
	t.Run("set pending after passing extensions", func(t *testing.T) {
		m, rec := newExtensionMigrator(t, declared,
			state("1.8", "1.8", false),
			state(nil, "1.2", false),
			state("1.6", "1.6", true),
			exists(true), history([]driver.Value{int64(1), "a", false}),
		)
		err := m.Verify(context.Background())
		if e, ok := errors.AsType[*migrate.SetError](err); !ok || e.Set != "app" || !errors.Is(err, migrate.ErrPending) {
			t.Fatalf("Verify = %v, want a pending *SetError naming app", err)
		}
		if _, ok := errors.AsType[*migrate.ExtensionError](err); ok {
			t.Errorf("Verify = %v, a set fault inside an *ExtensionError", err)
		}
		assertOps(t, rec, slices.Repeat([]sqltest.Op{sqltest.OpQuery}, 5)...)
		assertNoLock(t, rec)
	})
	t.Run("extension fault before a pending set", func(t *testing.T) {
		m, rec := newExtensionMigrator(t, declared,
			state("1.8", "1.8", false),
			state("1.0", "1.2", false),
			exists(false),
		)
		err := m.Verify(context.Background())
		if e, ok := errors.AsType[*migrate.ExtensionError](err); !ok || e.Extension != "gone" || !errors.Is(err, migrate.ErrPending) {
			t.Fatalf("Verify = %v, want a pending *ExtensionError naming gone", err)
		}
		if _, ok := errors.AsType[*migrate.SetError](err); ok {
			t.Errorf("Verify = %v, reported the set", err)
		}
		if got := len(rec.Calls()); got != 2 {
			t.Errorf("Verify made %d calls, want the two state reads", got)
		}
		if err := m.Layers()[0].Verify(context.Background()); !errors.Is(err, migrate.ErrPending) {
			t.Errorf("Layer.Verify = %v, want the set pending", err)
		}
		assertOps(t, rec, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery)
		assertNoLock(t, rec)
	})
}

func TestVerify_StateFailureNamesTheExtension(t *testing.T) {
	m, rec := newExtensionMigrator(t, []migrate.Extension{{Name: "hstore"}}, failed)
	err := m.Verify(context.Background())
	if !errors.Is(err, errDriver) {
		t.Fatalf("Verify = %v, want the engine error", err)
	}
	if e, ok := errors.AsType[*migrate.ExtensionError](err); !ok || e.Extension != "hstore" {
		t.Errorf("Verify = %v, want an *ExtensionError naming hstore", err)
	}
	assertOps(t, rec, sqltest.OpQuery)
}

func TestLayerVerifyAndStatus_ReadNoExtension(t *testing.T) {
	m, rec := newExtensionMigrator(t, []migrate.Extension{{Name: "pg_trgm"}, {Name: "old", Removed: true}},
		exists(true), allApplied,
		exists(true), allApplied,
	)
	if err := m.Layers()[0].Verify(context.Background()); err != nil {
		t.Errorf("Layer.Verify = %v", err)
	}
	if _, err := m.Status(context.Background()); err != nil {
		t.Errorf("Status = %v", err)
	}
	assertOps(t, rec, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery, sqltest.OpQuery)
	assertNoExtensionStatement(t, rec)
}

func assertNoLock(t *testing.T, rec *sqltest.Recorder) {
	t.Helper()
	for _, c := range rec.Calls() {
		if strings.Contains(c.SQL, "lock(") {
			t.Errorf("ran lock statement %q", c.SQL)
		}
	}
}
