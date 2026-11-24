//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	"github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/postgres/pgtest"
)

var _ migrate.ExtensionCatalog = postgres.Dialect{}

const dsnEnv = "SQLATE_DSN"

var hstore = []migrate.Extension{{Name: "hstore"}}

func extensionSet(migrations ...migrate.Migration) []migrate.Set {
	return []migrate.Set{{Name: "ext", Table: "ext_history", Migrations: migrations}}
}

var docTable = migrate.Migration{
	Version: 1, Name: "doc",
	Up:   "CREATE TABLE doc (id integer PRIMARY KEY, attrs hstore)",
	Down: "DROP TABLE doc", Transactional: true,
}

func installed(t testing.TB, db *sqlate.DB, name string) string {
	t.Helper()
	return text(t, db, "SELECT COALESCE((SELECT extversion FROM pg_extension WHERE extname = $1), '')", name)
}

func statusOf(t testing.TB, m *migrate.Migrator) migrate.ExtensionStatus {
	t.Helper()
	got, err := m.Extensions(context.Background())
	if err != nil {
		t.Fatalf("Extensions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Extensions = %+v, want one status", got)
	}
	return got[0]
}

func TestLive_ExtensionBehindTheDefaultIsPendingUntilUp(t *testing.T) {
	ctx := context.Background()
	d := pgtest.Create(t, dsnEnv, "ext")
	db := d.Session()
	if _, err := db.ExecContext(ctx, "CREATE EXTENSION hstore VERSION '1.4'"); err != nil {
		t.Fatalf("install hstore 1.4: %v", err)
	}
	var logged bytes.Buffer
	m := migratorOver(t, db, extensionSet(docTable), migrate.Options{
		Extensions: hstore,
		Logger:     slog.New(slog.NewTextHandler(&logged, nil)),
	})

	err := m.Verify(ctx)
	if !errors.Is(err, migrate.ErrPending) {
		t.Fatalf("Verify with hstore at 1.4 = %v, want ErrPending", err)
	}
	if extErr, ok := errors.AsType[*migrate.ExtensionError](err); !ok || extErr.Extension != "hstore" {
		t.Errorf("Verify = %v, want an *ExtensionError naming hstore", err)
	}
	if pending, ok := errors.AsType[*migrate.ExtensionPendingError](err); !ok || pending.Installed != "1.4" || pending.Default != "1.8" {
		t.Errorf("Verify = %v, want an *ExtensionPendingError from 1.4 to 1.8", err)
	}
	want := migrate.ExtensionStatus{Name: "hstore", Installed: "1.4", Default: "1.8", Pending: true}
	if got := statusOf(t, m); got != want {
		t.Errorf("Extensions before Up = %+v, want %+v", got, want)
	}

	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if got := installed(t, db, "hstore"); got != "1.8" {
		t.Errorf("hstore after Up is at %q, want 1.8", got)
	}
	want = migrate.ExtensionStatus{Name: "hstore", Installed: "1.8", Default: "1.8"}
	if got := statusOf(t, m); got != want {
		t.Errorf("Extensions after Up = %+v, want %+v", got, want)
	}
	if err := m.Verify(ctx); err != nil {
		t.Errorf("Verify after Up: %v", err)
	}
	if !strings.Contains(logged.String(), `msg="extension activated" extension=hstore version=1.8 from=1.4`) {
		t.Errorf("the log does not record the update from 1.4 to 1.8:\n%s", logged.String())
	}
}

func TestLive_UnavailableExtensionRefusesUpBeforeAnySet(t *testing.T) {
	ctx := context.Background()
	d := pgtest.Create(t, dsnEnv, "ext")
	db := d.Session()
	m := migratorOver(t, db, extensionSet(migrate.Migration{
		Version: 1, Name: "marker", Up: "CREATE TABLE marker (id integer)", Down: "DROP TABLE marker", Transactional: true,
	}), migrate.Options{Extensions: []migrate.Extension{{Name: "sqlate_absent"}}})

	err := m.Up(ctx)
	if !errors.Is(err, migrate.ErrExtensionUnavailable) {
		t.Fatalf("Up with an extension the image lacks = %v, want ErrExtensionUnavailable", err)
	}
	if extErr, ok := errors.AsType[*migrate.ExtensionError](err); !ok || extErr.Extension != "sqlate_absent" {
		t.Errorf("Up = %v, want an *ExtensionError naming sqlate_absent", err)
	}
	if v, err := m.Version(ctx); err != nil || v != (migrate.Version{}) {
		t.Errorf("Version after the refused Up = %+v, %v; want no history row", v, err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM ext_history"); n != 0 {
		t.Errorf("history holds %d rows after the refused Up, want none", n)
	}
	if got := text(t, db, "SELECT COALESCE(CAST(to_regclass('marker') AS text), '')"); got != "" {
		t.Errorf("the set's table %q exists after the refused Up", got)
	}
}

func TestLive_RemovedExtensionWaitsForItsDependentColumn(t *testing.T) {
	ctx := context.Background()
	d := pgtest.MigratedWith(t, dsnEnv, "ext", migrate.Options{Extensions: hstore}, extensionSet(docTable)...)
	db := d.Session()
	if got := installed(t, db, "hstore"); got != "1.8" {
		t.Fatalf("hstore after MigratedWith is at %q, want 1.8", got)
	}
	removed := migrate.Options{Extensions: []migrate.Extension{{Name: "hstore", Removed: true}}}

	blocked := migratorOver(t, db, extensionSet(docTable), removed)
	err := blocked.Up(ctx)
	extErr, ok := errors.AsType[*migrate.ExtensionError](err)
	if !ok || extErr.Extension != "hstore" {
		t.Fatalf("Up removing hstore under a dependent column = %v, want an *ExtensionError naming hstore", err)
	}
	if !errors.Is(err, sqlate.ErrDependentObjects) {
		t.Errorf("Up = %v, want it over sqlate.ErrDependentObjects", err)
	}
	if got := installed(t, db, "hstore"); got != "1.8" {
		t.Errorf("hstore after the refused removal is at %q, want 1.8 still", got)
	}
	if got := statusOf(t, blocked); !got.Pending || !got.Removed {
		t.Errorf("Extensions after the refused removal = %+v, want a pending removal", got)
	}

	dropColumn := migrate.Migration{
		Version: 2, Name: "drop attrs",
		Up:   "ALTER TABLE doc DROP COLUMN attrs",
		Down: "ALTER TABLE doc ADD COLUMN attrs hstore", Transactional: true,
	}
	var logged bytes.Buffer
	removed.Logger = slog.New(slog.NewTextHandler(&logged, nil))
	m := migratorOver(t, db, extensionSet(docTable, dropColumn), removed)
	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up after the column's drop: %v", err)
	}
	if got := installed(t, db, "hstore"); got != "" {
		t.Errorf("hstore after Up is at %q, want it dropped", got)
	}
	if v, err := m.Version(ctx); err != nil || v.Version != 2 {
		t.Errorf("Version = %+v, %v; want 2", v, err)
	}
	if !strings.Contains(logged.String(), `msg="extension removed" extension=hstore version=1.8`) {
		t.Errorf("the log does not record hstore's removal:\n%s", logged.String())
	}
	if err := m.Verify(ctx); err != nil {
		t.Errorf("Verify after the removal: %v", err)
	}
}

func TestLive_ResetLeavesTheExtensionInstalled(t *testing.T) {
	ctx := context.Background()
	opts := migrate.Options{Extensions: hstore}
	d := pgtest.MigratedWith(t, dsnEnv, "ext", opts, extensionSet(docTable)...)
	db := d.Session()
	m := migratorOver(t, db, extensionSet(docTable), opts)
	if err := m.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := text(t, db, "SELECT COALESCE(CAST(to_regclass('doc') AS text), '')"); got != "" {
		t.Fatalf("Reset left the set's table %q; the proof starts from the wrong state", got)
	}
	if got := installed(t, db, "hstore"); got != "1.8" {
		t.Errorf("hstore after Reset is at %q, want 1.8", got)
	}
}

func TestLive_ConcurrentUpsActivateOnce(t *testing.T) {
	ctx := context.Background()
	d := pgtest.Create(t, dsnEnv, "ext")
	db := d.Session()
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))
	opts := migrate.Options{Extensions: hstore, Logger: logger}
	runners := []*migrate.Migrator{
		migratorOver(t, db, extensionSet(docTable), opts),
		migratorOver(t, db, extensionSet(docTable), opts),
	}

	errs := make([]error, len(runners))
	var wg sync.WaitGroup
	for i, m := range runners {
		wg.Go(func() { errs[i] = m.Up(ctx) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Up %d: %v", i, err)
		}
	}
	if n := strings.Count(logged.String(), `msg="extension activated"`); n != 1 {
		t.Errorf("hstore activated %d times over two concurrent runs, want once:\n%s", n, logged.String())
	}
	if n := strings.Count(logged.String(), `msg="migration applied"`); n != 1 {
		t.Errorf("the set's migration applied %d times over two concurrent runs, want once:\n%s", n, logged.String())
	}
	if got := installed(t, db, "hstore"); got != "1.8" {
		t.Errorf("hstore after the runs is at %q, want 1.8", got)
	}
}
