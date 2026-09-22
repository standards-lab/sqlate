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

// The two sets every multi-set proof declares: a library set over its own
// history table and the program's own set over the default one, the library
// declared first because the program's migrations reference its objects.
var (
	library = migrate.Set{
		Name:  "lib",
		Table: "lib_schema_version",
		Migrations: []migrate.Migration{
			{Version: 1, Name: "lib_one", Up: "CREATE TABLE lib_one ()", Down: "DROP TABLE lib_one", Transactional: true},
		},
	}
	app = migrate.Set{
		Name: "app",
		Migrations: []migrate.Migration{
			{Version: 1, Name: "app_one", Up: "CREATE TABLE app_one ()", Down: "DROP TABLE app_one", Transactional: true},
		},
	}
	// two is a set of two migrations, for the states one migration cannot
	// show: a partly applied history.
	two = migrate.Set{Name: "two", Table: "two_schema_version", Migrations: set}

	libApplied = [][]driver.Value{{int64(1), "lib_one", false}}
	appApplied = [][]driver.Value{{int64(1), "app_one", false}}

	// step scripts one statement of a migration or of its history row.
	step = sqltest.Response{}
)

// migrationTexts are the two sets' up and down texts, the statements a
// refused run must not have executed.
var migrationTexts = []string{
	"CREATE TABLE lib_one ()", "DROP TABLE lib_one",
	"CREATE TABLE app_one ()", "DROP TABLE app_one",
}

// newSets builds a migrator over the declared sets and the driver fake with
// the lock capability, so lock and unlock calls are part of the script.
func newSets(t *testing.T, sets []migrate.Set, responses ...sqltest.Response) (*migrate.Migrator, *sqltest.Recorder) {
	t.Helper()
	pool, rec := sqltest.Open(t, responses...)
	m, err := migrate.New(sqlate.Wrap(pool, lockingDialect{}), sets, migrate.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m, rec
}

// script flattens the parts of a response sequence.
func script(parts ...[]sqltest.Response) []sqltest.Response {
	var out []sqltest.Response
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func one(r ...sqltest.Response) []sqltest.Response { return r }

// checked scripts one set's pre-flight: the history table's create and the
// ordered read returning applied.
func checked(applied ...[]driver.Value) []sqltest.Response {
	return []sqltest.Response{created, history(applied...)}
}

// assertPrefixes checks that got has one text per want, in order, each
// starting with its want.
func assertPrefixes(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("exec texts:\n%s\nwant %d texts", strings.Join(got, "\n"), len(want))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("exec %d = %q, want prefix %q", i, got[i], want[i])
		}
	}
}

// assertOneLock checks that the run took exactly one lock, under name, as
// its first call, and released it as its last.
func assertOneLock(t *testing.T, rec *sqltest.Recorder, name string) {
	t.Helper()
	calls := rec.Calls()
	var locks []sqltest.Call
	for _, c := range calls {
		if strings.HasPrefix(c.SQL, "SELECT lock(") {
			locks = append(locks, c)
		}
	}
	if len(locks) != 1 || len(locks[0].Args) != 1 || locks[0].Args[0] != name {
		t.Fatalf("lock calls = %+v, want one under %q", locks, name)
	}
	if calls[0].SQL != "SELECT lock($1)" {
		t.Errorf("the first call is %q, want the lock", calls[0].SQL)
	}
	if last := calls[len(calls)-1]; last.SQL != "SELECT unlock($1)" || last.Args[0] != name {
		t.Errorf("the last call is %+v, want the unlock of %q", last, name)
	}
}

// assertNoMigrationText checks that no set's up or down text ran.
func assertNoMigrationText(t *testing.T, rec *sqltest.Recorder) {
	t.Helper()
	for _, text := range rec.SQL(sqltest.OpExec) {
		if slices.Contains(migrationTexts, text) {
			t.Errorf("the run executed %q", text)
		}
	}
}

// TestNewValidation covers the checks New makes before any I/O: a database,
// at least one set, a name on every set, no repeated name, no shared
// history table (an empty table and the explicit default are the same
// table), a table name that is a plain identifier, and each set's own
// migration ordering.
func TestNewValidation(t *testing.T) {
	pool, _ := sqltest.Open(t)
	db := sqlate.Wrap(pool, lockingDialect{})
	cases := []struct {
		name string
		sets []migrate.Set
		want string
	}{
		{"no sets", nil, "no sets"},
		{"unnamed", []migrate.Set{{Table: "t"}}, "a set has no name"},
		{"repeated name", []migrate.Set{library, {Name: "lib", Table: "other"}}, `set "lib" is declared twice`},
		{"shared table", []migrate.Set{app, {Name: "other", Table: "schema_version"}}, `share the history table "schema_version"`},
		{"both default", []migrate.Set{app, {Name: "other"}}, `share the history table "schema_version"`},
		{"bad table name", []migrate.Set{{Name: "x", Table: "bad name; drop"}}, `is not a plain identifier`},
		{
			"bad migrations",
			[]migrate.Set{{Name: "x", Migrations: []migrate.Migration{{Version: 0, Name: "z", Up: "u"}}}},
			`set "x": version 0 must be positive`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := migrate.New(db, c.sets, migrate.Options{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("New = %v, want an error containing %q", err, c.want)
			}
		})
	}
	if _, err := migrate.New(db, []migrate.Set{library, app}, migrate.Options{}); err != nil {
		t.Fatalf("New(valid sets) = %v", err)
	}
	if _, err := migrate.New(nil, []migrate.Set{library}, migrate.Options{}); err == nil {
		t.Fatal("New(nil db) succeeded")
	}
}

// TestUpOrder proves Up takes one lock, creates and reads every set's
// history before any migration runs, then applies the sets in declared
// order, each over its own history table.
func TestUpOrder(t *testing.T) {
	responses := script(one(locked), checked(), checked(), one(step, step, step, step), one(unlocked))
	m, rec := newSets(t, []migrate.Set{library, app}, responses...)
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if rec.Pending() != 0 {
		t.Errorf("%d scripted responses unconsumed", rec.Pending())
	}
	assertPrefixes(t, rec.SQL(sqltest.OpExec), []string{
		"SELECT lock($1)",
		"CREATE TABLE IF NOT EXISTS lib_schema_version",
		"CREATE TABLE IF NOT EXISTS schema_version",
		"CREATE TABLE lib_one ()", "INSERT INTO lib_schema_version",
		"CREATE TABLE app_one ()", "INSERT INTO schema_version",
	})
	assertPrefixes(t, rec.SQL(sqltest.OpQuery), []string{
		"SELECT version, name, dirty FROM lib_schema_version",
		"SELECT version, name, dirty FROM schema_version",
		"SELECT unlock($1)",
	})
	assertOneLock(t, rec, "migrate.schema_version")
}

// TestLockName proves the lock name defaults to "migrate." over the top
// set's history table, and that Options.LockName overrides it.
func TestLockName(t *testing.T) {
	responses := script(one(locked), checked(), checked(), one(step, step, step, step), one(unlocked))
	m, rec := newSets(t, []migrate.Set{app, library}, responses...)
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	// The sets are declared the other way round here, so the default names
	// the library's table: the top set's, not the first set's.
	assertOneLock(t, rec, "migrate.lib_schema_version")

	pool, rec := sqltest.Open(t, script(one(locked), checked(), one(step, step), one(unlocked))...)
	m, err := migrate.New(sqlate.Wrap(pool, lockingDialect{}), []migrate.Set{library}, migrate.Options{LockName: "acme.schema"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	assertOneLock(t, rec, "acme.schema")
}

// TestNoLocker proves a dialect without the lock capability refuses a run
// with ErrNoLocker before any call, and runs without a lock under
// Options.Unlocked.
func TestNoLocker(t *testing.T) {
	pool, rec := sqltest.Open(t)
	m, err := migrate.New(sqlate.Wrap(pool, sqltest.Dialect{}), []migrate.Set{library, app}, migrate.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Up(context.Background()); !errors.Is(err, migrate.ErrNoLocker) {
		t.Fatalf("Up on a dialect without a locker = %v, want ErrNoLocker", err)
	}
	if len(rec.Calls()) != 0 {
		t.Errorf("calls made before refusing: %v", rec.Ops())
	}

	pool, rec = sqltest.Open(t, script(checked(), one(step, step))...)
	m, err = migrate.New(sqlate.Wrap(pool, sqltest.Dialect{}), []migrate.Set{library}, migrate.Options{Unlocked: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("unlocked Up: %v", err)
	}
	if rec.Pending() != 0 {
		t.Errorf("%d scripted responses unconsumed", rec.Pending())
	}
	for _, text := range rec.SQL(sqltest.OpExec) {
		if strings.HasPrefix(text, "SELECT lock") {
			t.Errorf("an unlocked run took a lock: %s", text)
		}
	}
}

// TestDown_RevertsTheTopSetOnly proves Migrator.Down reverts the top set
// and leaves the sets below it applied, with no history table dropped.
func TestDown_RevertsTheTopSetOnly(t *testing.T) {
	responses := script(one(locked), checked(libApplied...), checked(appApplied...), one(step, step), one(unlocked))
	m, rec := newSets(t, []migrate.Set{library, app}, responses...)
	if err := m.Down(context.Background(), 1); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if rec.Pending() != 0 {
		t.Errorf("%d scripted responses unconsumed", rec.Pending())
	}
	assertPrefixes(t, rec.SQL(sqltest.OpExec), []string{
		"SELECT lock($1)",
		"CREATE TABLE IF NOT EXISTS lib_schema_version",
		"CREATE TABLE IF NOT EXISTS schema_version",
		"DROP TABLE app_one", "DELETE FROM schema_version WHERE version =",
	})
}

// TestDown_RefusesALayerBelowAnAppliedOne proves a revert of the library
// set is refused with ErrAboveApplied while the program's set is still
// applied, naming both layers, with no migration text run.
func TestDown_RefusesALayerBelowAnAppliedOne(t *testing.T) {
	responses := script(one(locked), checked(libApplied...), checked(appApplied...), one(unlocked))
	m, rec := newSets(t, []migrate.Set{library, app}, responses...)
	lib, ok := m.Layer("lib")
	if !ok {
		t.Fatal(`Layer("lib") not found`)
	}
	err := lib.Down(context.Background(), 1)
	if !errors.Is(err, migrate.ErrAboveApplied) {
		t.Fatalf("Down = %v, want ErrAboveApplied", err)
	}
	if msg := err.Error(); !strings.Contains(msg, `"app" above "lib"`) {
		t.Errorf("Down = %q, want it to name both layers", msg)
	}
	assertNoMigrationText(t, rec)
	if rec.Pending() != 0 {
		t.Errorf("%d scripted responses unconsumed", rec.Pending())
	}
}

// TestSteps_RefusesALayerAboveAPendingOne proves an apply on the program's
// set is refused with ErrBelowPending while the library set still has
// pending migrations, naming both layers, with no migration text run.
func TestSteps_RefusesALayerAboveAPendingOne(t *testing.T) {
	responses := script(one(locked), checked(), checked(), one(unlocked))
	m, rec := newSets(t, []migrate.Set{library, app}, responses...)
	top, ok := m.Layer("app")
	if !ok {
		t.Fatal(`Layer("app") not found`)
	}
	err := top.Steps(context.Background(), 1)
	if !errors.Is(err, migrate.ErrBelowPending) {
		t.Fatalf("Steps = %v, want ErrBelowPending", err)
	}
	if msg := err.Error(); !strings.Contains(msg, `"lib" below "app"`) {
		t.Errorf("Steps = %q, want it to name both layers", msg)
	}
	assertNoMigrationText(t, rec)
	if rec.Pending() != 0 {
		t.Errorf("%d scripted responses unconsumed", rec.Pending())
	}
}

// TestResetOrder proves Reset reverts the sets in reverse declared order
// and drops each set's history table once that set is reverted, all under
// the one lock.
func TestResetOrder(t *testing.T) {
	responses := script(
		one(locked), checked(libApplied...), checked(appApplied...),
		one(step, step, step), one(step, step, step), one(unlocked),
	)
	m, rec := newSets(t, []migrate.Set{library, app}, responses...)
	if err := m.Reset(context.Background()); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if rec.Pending() != 0 {
		t.Errorf("%d scripted responses unconsumed", rec.Pending())
	}
	assertPrefixes(t, rec.SQL(sqltest.OpExec), []string{
		"SELECT lock($1)",
		"CREATE TABLE IF NOT EXISTS lib_schema_version",
		"CREATE TABLE IF NOT EXISTS schema_version",
		"DROP TABLE app_one", "DELETE FROM schema_version WHERE version =", "DROP TABLE schema_version",
		"DROP TABLE lib_one", "DELETE FROM lib_schema_version WHERE version =", "DROP TABLE lib_schema_version",
	})
	assertOneLock(t, rec, "migrate.schema_version")
}

// TestUpStopsAtFailure proves a failing set stops the run before the sets
// after it, the error names the set, and the lock is released.
func TestUpStopsAtFailure(t *testing.T) {
	responses := script(
		one(locked), checked(), checked(),
		one(sqltest.Response{Err: errDriver}), one(unlocked),
	)
	m, rec := newSets(t, []migrate.Set{library, app}, responses...)
	err := m.Up(context.Background())
	se, ok := errors.AsType[*migrate.SetError](err)
	if !ok || se.Set != "lib" {
		t.Fatalf("Up = %v, want a SetError naming set lib", err)
	}
	if !errors.Is(err, errDriver) {
		t.Errorf("Up = %v, want it to wrap the engine error", err)
	}
	if texts := rec.SQL(sqltest.OpExec); slices.Contains(texts, "CREATE TABLE app_one ()") {
		t.Errorf("the app set ran after the library set failed: %v", texts)
	}
	if slices.Contains(rec.Ops(), sqltest.OpCommit) {
		t.Error("the failed transaction committed")
	}
	if rec.Pending() != 0 {
		t.Errorf("%d scripted responses unconsumed; the unlock did not run", rec.Pending())
	}
}

// TestDirtyRefusal proves every mutating verb refuses a dirty set before
// any set runs, even when the dirty set is not the first: the library set
// is clean and pending, the program's history has a dirty row, and no
// migration text of either set runs. The error names the set, classifies as
// ErrDirty, and carries the version.
func TestDirtyRefusal(t *testing.T) {
	dirty := [][]driver.Value{{int64(1), "app_one", true}}
	for _, op := range []struct {
		name string
		run  func(*migrate.Migrator, context.Context) error
	}{
		{"Up", (*migrate.Migrator).Up},
		{"Reset", (*migrate.Migrator).Reset},
		{"Down", func(m *migrate.Migrator, ctx context.Context) error { return m.Down(ctx, 1) }},
		{"Steps forward", func(m *migrate.Migrator, ctx context.Context) error { return m.Steps(ctx, 1) }},
		{"Steps back", func(m *migrate.Migrator, ctx context.Context) error { return m.Steps(ctx, -1) }},
	} {
		t.Run(op.name, func(t *testing.T) {
			responses := script(one(locked), checked(), checked(dirty...), one(unlocked))
			m, rec := newSets(t, []migrate.Set{library, app}, responses...)
			err := op.run(m, context.Background())
			if !errors.Is(err, migrate.ErrDirty) {
				t.Fatalf("%s = %v, want ErrDirty", op.name, err)
			}
			se, ok := errors.AsType[*migrate.SetError](err)
			if !ok || se.Set != "app" {
				t.Errorf("%s = %v, want a SetError naming set app", op.name, err)
			}
			de, ok := errors.AsType[*migrate.DirtyError](err)
			if !ok || de.Version != 1 {
				t.Errorf("%s = %v, want the dirty version 1", op.name, err)
			}
			assertNoMigrationText(t, rec)
			for _, text := range rec.SQL(sqltest.OpExec) {
				if strings.HasPrefix(text, "DROP TABLE") {
					t.Errorf("%s dropped %q after the dirty check", op.name, text)
				}
			}
			if rec.Pending() != 0 {
				t.Errorf("%d scripted responses unconsumed", rec.Pending())
			}
		})
	}
}

// TestUnknownHistoryRefusal proves a history row the set does not carry
// refuses the run, classified as ErrUnknownVersion and naming the set.
func TestUnknownHistoryRefusal(t *testing.T) {
	responses := script(one(locked), checked([]driver.Value{int64(1), "someone_elses", false}), one(unlocked))
	m, rec := newSets(t, []migrate.Set{library}, responses...)
	err := m.Up(context.Background())
	se, ok := errors.AsType[*migrate.SetError](err)
	if !errors.Is(err, migrate.ErrUnknownVersion) || !ok || se.Set != "lib" {
		t.Fatalf("Up = %v, want a SetError for lib classified as ErrUnknownVersion", err)
	}
	assertNoMigrationText(t, rec)
	if rec.Pending() != 0 {
		t.Errorf("%d scripted responses unconsumed", rec.Pending())
	}
}

// TestStatus proves Status reads each set in declared order without a lock,
// two calls per set, and reports every field of SetStatus.
func TestStatus(t *testing.T) {
	ctx := context.Background()

	// The library set at its head and the program's set with no history
	// table: the read of a missing table is the existence check alone.
	m, rec := newSets(t, []migrate.Set{library, app},
		exists(true), history(libApplied...), exists(false))
	got, err := m.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Status returned %d sets, want 2", len(got))
	}
	lib, ap := got[0], got[1]
	if lib.Name != "lib" || lib.Table != "lib_schema_version" || lib.Version != 1 || lib.Latest != 1 || len(lib.Pending) != 0 || lib.Dirty {
		t.Errorf("lib status = %+v", lib)
	}
	if ap.Name != "app" || ap.Table != "schema_version" || ap.Version != 0 || ap.Latest != 1 || len(ap.Pending) != 1 || ap.Pending[0].Name != "app_one" || ap.Dirty {
		t.Errorf("app status = %+v", ap)
	}
	for _, text := range rec.SQL(sqltest.OpExec) {
		if strings.HasPrefix(text, "SELECT lock") {
			t.Errorf("Status took a lock: %s", text)
		}
	}
	if rec.Pending() != 0 {
		t.Errorf("%d scripted responses unconsumed", rec.Pending())
	}

	// A partly applied set: the head is the applied version and the rest of
	// the set is pending.
	m, _ = newSets(t, []migrate.Set{two}, exists(true), history([]driver.Value{int64(1), "a", false}))
	got, err = m.Status(ctx)
	if err != nil {
		t.Fatalf("partial Status: %v", err)
	}
	if s := got[0]; s.Version != 1 || s.Latest != 2 || len(s.Pending) != 1 || s.Pending[0].Version != 2 {
		t.Errorf("partial status = %+v, want version 1 of 2 with 2 pending", s)
	}

	// A dirty row is state Status reports, not a failure.
	m, _ = newSets(t, []migrate.Set{library}, exists(true), history([]driver.Value{int64(1), "lib_one", true}))
	got, err = m.Status(ctx)
	if err != nil || len(got) != 1 || !got[0].Dirty || got[0].Version != 1 {
		t.Errorf("dirty Status = %+v, %v; want version 1 dirty", got, err)
	}

	// A history row the set does not carry is an error naming the set.
	m, _ = newSets(t, []migrate.Set{library}, exists(true), history([]driver.Value{int64(1), "other", false}))
	_, err = m.Status(ctx)
	se, ok := errors.AsType[*migrate.SetError](err)
	if !errors.Is(err, migrate.ErrUnknownVersion) || !ok || se.Set != "lib" {
		t.Errorf("Status over a mismatched history = %v, want a SetError for lib", err)
	}
}

// TestForce proves Layer.Force runs the named set's override under the
// lock, that Layer reports a name the migrator does not hold, and that
// Migrator.Force acts on the top set.
func TestForce(t *testing.T) {
	ctx := context.Background()
	m, rec := newSets(t, []migrate.Set{app, library}, script(one(locked), one(created, step), one(unlocked))...)
	top, ok := m.Layer("app")
	if !ok {
		t.Fatal(`Layer("app") not found`)
	}
	if err := top.Force(ctx, 0); err != nil {
		t.Fatalf("Force: %v", err)
	}
	assertPrefixes(t, rec.SQL(sqltest.OpExec), []string{
		"SELECT lock($1)",
		"CREATE TABLE IF NOT EXISTS schema_version",
		"DELETE FROM schema_version WHERE version >",
	})
	if rec.Pending() != 0 {
		t.Errorf("%d scripted responses unconsumed", rec.Pending())
	}
	if _, ok := m.Layer("nope"); ok {
		t.Error(`Layer("nope") reported a set the migrator does not hold`)
	}

	// Migrator.Force acts on the top set, the library here, not the first.
	m, rec = newSets(t, []migrate.Set{app, library}, script(one(locked), one(created, step), one(unlocked))...)
	if err := m.Force(ctx, 0); err != nil {
		t.Fatalf("Migrator.Force: %v", err)
	}
	assertPrefixes(t, rec.SQL(sqltest.OpExec), []string{
		"SELECT lock($1)",
		"CREATE TABLE IF NOT EXISTS lib_schema_version",
		"DELETE FROM lib_schema_version WHERE version >",
	})
}

// TestLayers proves Layers reports every set in declared order and that
// Migrations and Version on the migrator read the top set.
func TestLayers(t *testing.T) {
	head := sqltest.Response{Columns: []string{"version", "dirty"}, Rows: [][]driver.Value{{int64(1), false}}}
	m, rec := newSets(t, []migrate.Set{library, app}, exists(true), head)

	layers := m.Layers()
	if len(layers) != 2 {
		t.Fatalf("Layers returned %d, want 2", len(layers))
	}
	if layers[0].Name() != "lib" || layers[0].Table() != "lib_schema_version" {
		t.Errorf("layer 0 = %q over %q", layers[0].Name(), layers[0].Table())
	}
	if layers[1].Name() != "app" || layers[1].Table() != "schema_version" {
		t.Errorf("layer 1 = %q over %q", layers[1].Name(), layers[1].Table())
	}

	if ms := m.Migrations(); len(ms) != 1 || ms[0].Name != "app_one" {
		t.Errorf("Migrations() = %+v, want the top set's", ms)
	}
	ms := m.Migrations()
	ms[0].Name = "mutated"
	if m.Migrations()[0].Name != "app_one" {
		t.Error("Migrations() exposed the internal slice")
	}

	v, err := m.Version(context.Background())
	if err != nil || v.Version != 1 || v.Dirty {
		t.Errorf("Version = %+v, %v", v, err)
	}
	if arg := rec.Calls()[0].Args[0]; arg != "schema_version" {
		t.Errorf("Version probed table %v, want the top set's", arg)
	}
	if rec.Pending() != 0 {
		t.Errorf("%d scripted responses unconsumed", rec.Pending())
	}
}

// TestVerify_CoversEverySetInOrder proves Verify reports the first set's
// fault and stops there: the library set is pending, so the program's set
// is never read.
func TestVerify_CoversEverySetInOrder(t *testing.T) {
	m, rec := newSets(t, []migrate.Set{library, app}, exists(false))
	err := m.Verify(context.Background())
	se, ok := errors.AsType[*migrate.SetError](err)
	if !ok || se.Set != "lib" {
		t.Fatalf("Verify = %v, want a SetError naming set lib", err)
	}
	pending, ok := errors.AsType[*migrate.PendingError](err)
	if !ok || !slices.Equal(pending.Versions, []int{1}) {
		t.Errorf("Verify = %v, want the library's one pending version", err)
	}
	if rec.Pending() != 0 {
		t.Errorf("Verify read past the first fault: %d responses unconsumed", rec.Pending())
	}
}
