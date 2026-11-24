package migrate_test

import (
	"context"
	"database/sql/driver"
	"errors"
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
	pool, rec := sqltest.Open(t, responses...)
	db := sqlate.Wrap(pool, extensionDialect{})
	m, err := migrate.New(db, []migrate.Set{{Name: "app", Migrations: set}}, migrate.Options{Extensions: declared})
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
		{Name: "old", Removed: true},
		{Name: "gone", Removed: true},
	}
	m, rec := newExtensionMigrator(t, declared,
		state("1.4", "1.8", true),
		state("1.6", "1.6", false),
		state(nil, "1.6", false),
		state(nil, nil, false),
		state("2.0", "1.8", false),
		state("1.0", "1.0", false),
		state(nil, "1.2", false),
	)
	got, err := m.Extensions(context.Background())
	if err != nil {
		t.Fatalf("Extensions: %v", err)
	}
	want := []migrate.ExtensionStatus{
		{Name: "hstore", Installed: "1.4", Default: "1.8", Pending: true},
		{Name: "citext", Installed: "1.6", Default: "1.6"},
		{Name: "pg_trgm", Default: "1.6", Pending: true},
		{Name: "absent", Pending: true},
		{Name: "newer", Installed: "2.0", Default: "1.8", Pending: true},
		{Name: "old", Removed: true, Installed: "1.0", Default: "1.0", Pending: true},
		{Name: "gone", Removed: true, Default: "1.2"},
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
		{Name: "hstore", Pending: true},
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
