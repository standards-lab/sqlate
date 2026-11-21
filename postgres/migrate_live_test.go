//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	"github.com/standards-lab/sqlate/postgres"
)

func scratch(t testing.TB, db *sqlate.DB, history string, tables ...string) {
	t.Helper()
	drop := func() {
		ctx := context.Background()
		for _, tb := range tables {
			_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+tb+" CASCADE")
		}
		_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+history)
	}
	drop()
	t.Cleanup(drop)
}

func migrator(t testing.TB, db *sqlate.DB, history string, set []migrate.Migration, opts migrate.Options) *migrate.Migrator {
	t.Helper()
	return migratorOver(t, db, []migrate.Set{{Name: "live", Table: history, Migrations: set}}, opts)
}

func migratorOver(t testing.TB, db *sqlate.DB, sets []migrate.Set, opts migrate.Options) *migrate.Migrator {
	t.Helper()
	m, err := migrate.New(db, sets, opts)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func indexValid(t testing.TB, db *sqlate.DB, index string) (exists, valid bool) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = $1", index)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return false, false
	}
	if err := rows.Scan(&valid); err != nil {
		t.Fatal(err)
	}
	return true, valid
}

func TestLive_NonTransactionalDDL(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	scratch(t, db, "live_ddl_history", "live_ddl")
	create := migrate.Migration{Version: 1, Name: "table", Up: "CREATE TABLE live_ddl (x int)", Down: "DROP TABLE live_ddl", Transactional: true}
	index := "CREATE INDEX CONCURRENTLY live_ddl_ix ON live_ddl (x)"

	inTx := migrator(t, db, "live_ddl_history", []migrate.Migration{create,
		{Version: 2, Name: "index", Up: index, Transactional: true}}, migrate.Options{})
	err := inTx.Up(ctx)
	if sqlState(err) != "25001" {
		t.Fatalf("CONCURRENTLY inside a transaction: err = %v, want SQLSTATE 25001", err)
	}
	if v, _ := inTx.Version(ctx); v.Version != 1 || v.Dirty {
		t.Fatalf("after the transactional failure: %+v, want clean at 1 (nothing recorded for 2)", v)
	}

	optOut := migrator(t, db, "live_ddl_history", []migrate.Migration{create,
		{Version: 2, Name: "index", Up: index, Down: "DROP INDEX CONCURRENTLY live_ddl_ix"}}, migrate.Options{})
	if err := optOut.Up(ctx); err != nil {
		t.Fatalf("opt-out Up: %v", err)
	}
	if exists, valid := indexValid(t, db, "live_ddl_ix"); !exists || !valid {
		t.Fatalf("index exists=%v valid=%v", exists, valid)
	}
	if v, _ := optOut.Version(ctx); v.Version != 2 || v.Dirty {
		t.Fatalf("head = %+v, want clean at 2", v)
	}
	if err := optOut.Down(ctx, 1); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if exists, _ := indexValid(t, db, "live_ddl_ix"); exists {
		t.Fatal("index survived Down")
	}
}

func TestLive_ForceFromEmptyWritesThePrefix(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	scratch(t, db, "live_force_history", "live_force_a", "live_force_b")
	set := []migrate.Migration{
		{Version: 1, Name: "a", Up: "CREATE TABLE live_force_a (x int)", Down: "DROP TABLE live_force_a", Transactional: true},
		{Version: 2, Name: "b", Up: "CREATE TABLE live_force_b (x int)", Down: "DROP TABLE live_force_b", Transactional: true},
	}
	m := migrator(t, db, "live_force_history", set, migrate.Options{})
	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}

	if err := m.Force(ctx, 0); err != nil {
		t.Fatalf("Force(0): %v", err)
	}
	if err := m.Force(ctx, 2); err != nil {
		t.Fatalf("Force(2): %v", err)
	}
	if err := m.Verify(ctx); err != nil {
		t.Errorf("Verify after Force(0), Force(2) = %v, want current", err)
	}
	st, err := m.Status(ctx)
	if err != nil || st[0].Version != 2 || len(st[0].Pending) != 0 {
		t.Errorf("Status = %+v, %v, want version 2 with nothing pending", st, err)
	}
	if err := m.Down(ctx, 2); err != nil {
		t.Errorf("Down(2) after the force = %v", err)
	}
}

func TestLive_DirtyStateAndForce(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	scratch(t, db, "live_dirty_history", "live_dirty")
	set := []migrate.Migration{
		{Version: 1, Name: "table", Up: "CREATE TABLE live_dirty (x int)", Down: "DROP TABLE live_dirty", Transactional: true},
		{Version: 2, Name: "rows", Up: "INSERT INTO live_dirty VALUES (1), (1)", Transactional: true},
		{Version: 3, Name: "unique", Up: "CREATE UNIQUE INDEX CONCURRENTLY live_dirty_uq ON live_dirty (x)", Down: "DROP INDEX IF EXISTS live_dirty_uq"},
	}
	m := migrator(t, db, "live_dirty_history", set, migrate.Options{})

	err := m.Up(ctx)
	var dirty *migrate.DirtyError
	if !errors.As(err, &dirty) || dirty.Version != 3 || !errors.Is(err, sqlate.ErrUniqueViolation) {
		t.Fatalf("Up = %v, want DirtyError{3} wrapping the unique violation", err)
	}
	if exists, valid := indexValid(t, db, "live_dirty_uq"); !exists || valid {
		t.Fatalf("orphan: exists=%v valid=%v, want an INVALID index left behind", exists, valid)
	}
	if v, _ := m.Version(ctx); v.Version != 3 || !v.Dirty {
		t.Fatalf("head = %+v, want dirty at 3", v)
	}
	if err := m.Verify(ctx); !errors.Is(err, migrate.ErrDirty) {
		t.Errorf("Verify = %v, want ErrDirty", err)
	}
	if err := m.Up(ctx); !errors.As(err, &dirty) || dirty.Err != nil {
		t.Errorf("second Up = %v, want the discovered DirtyError", err)
	}

	if _, err := db.ExecContext(ctx, "DROP INDEX live_dirty_uq"); err != nil {
		t.Fatal(err)
	}
	if err := m.Force(ctx, 2); err != nil {
		t.Fatalf("Force(2): %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM live_dirty WHERE ctid = (SELECT MIN(ctid) FROM live_dirty)"); err != nil {
		t.Fatal(err)
	}
	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up after repair: %v", err)
	}
	if exists, valid := indexValid(t, db, "live_dirty_uq"); !exists || !valid {
		t.Fatalf("after repair: exists=%v valid=%v", exists, valid)
	}
	if err := m.Verify(ctx); err != nil {
		t.Errorf("Verify after repair: %v", err)
	}
}

var slowSet = []migrate.Migration{
	{Version: 1, Name: "slow", Up: "SELECT pg_sleep(0.4)", Down: "SELECT 1", Transactional: true},
	{Version: 2, Name: "table", Up: "CREATE TABLE live_race (x int)", Down: "DROP TABLE live_race", Transactional: true},
}

func TestLive_ConcurrentStartersInProcess(t *testing.T) {
	db := live(t)
	scratch(t, db, "live_race_history", "live_race")
	errs := make([]error, 4)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() {
			errs[i] = migrator(t, db, "live_race_history", slowSet, migrate.Options{}).Up(context.Background())
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("starter %d: %v", i, err)
		}
	}
	assertAppliedOnce(t, db, "live_race_history", len(slowSet))
}

func TestLive_UnlockedStartersCollide(t *testing.T) {
	db := live(t)
	scratch(t, db, "live_race_history", "live_race")
	errs := make([]error, 4)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() {
			errs[i] = migrator(t, db, "live_race_history", slowSet, migrate.Options{Unlocked: true}).Up(context.Background())
		})
	}
	wg.Wait()
	failed := 0
	for _, err := range errs {
		if err == nil {
			continue
		}
		if st := sqlState(err); st != "42P07" && st != "23505" {
			t.Errorf("unexpected failure: %v (SQLSTATE %s)", err, st)
		}
		failed++
	}
	if failed == 0 {
		t.Fatal("unlocked starters did not collide; the lock is proving nothing")
	}
	t.Logf("unlocked: %d of 4 starters collided", failed)
}

func TestLive_ConcurrentStartersAcrossProcesses(t *testing.T) {
	db := live(t)
	scratch(t, db, "live_race_history", "live_race")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmds := make([]*exec.Cmd, 4)
	for i := range cmds {
		cmd := exec.Command(exe, "-test.run=^TestLive_Helper$", "-test.v")
		cmd.Env = append(os.Environ(), "SQLATE_HELPER=starter")
		cmds[i] = cmd
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Errorf("process %d: %v", i, err)
		}
	}
	assertAppliedOnce(t, db, "live_race_history", len(slowSet))
}

func TestLive_Helper(t *testing.T) {
	if os.Getenv("SQLATE_HELPER") != "starter" {
		t.Skip("helper")
	}
	db := live(t)
	if err := migrator(t, db, "live_race_history", slowSet, migrate.Options{}).Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
}

func advisoryLocks(t testing.TB, db *sqlate.DB) int {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory'")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var n int
	if !rows.Next() {
		t.Fatal("no row")
	}
	if err := rows.Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func assertAppliedOnce(t testing.TB, db *sqlate.DB, history string, want int) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "SELECT COUNT(*), COUNT(DISTINCT version), BOOL_OR(dirty) FROM "+history)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var n, distinct int
	var dirty bool
	if !rows.Next() {
		t.Fatal("no history")
	}
	if err := rows.Scan(&n, &distinct, &dirty); err != nil {
		t.Fatal(err)
	}
	if n != want || distinct != want || dirty {
		t.Errorf("history rows=%d distinct=%d dirty=%v, want %d applied once, clean", n, distinct, dirty, want)
	}
}

func TestLive_CancelledContext(t *testing.T) {
	db := live(t)
	scratch(t, db, "live_cancel_history")
	set := []migrate.Migration{{Version: 1, Name: "slow", Up: "SELECT pg_sleep(5)", Transactional: true}}
	m := migrator(t, db, "live_cancel_history", set, migrate.Options{})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := m.Up(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Up = %v, want the deadline", err)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Errorf("Up took %v after a 300ms deadline", took)
	}
	t.Logf("cancelled run reported: %v", err)
	if msg := err.Error(); strings.Contains(msg, "rollback") || strings.Contains(msg, "unlock") || errors.Is(err, postgres.ErrLockNotHeld) {
		t.Errorf("the cancellation reported connection noise: %v", err)
	}
	if v, verr := m.Version(context.Background()); verr != nil || v.Version != 0 {
		t.Errorf("after cancellation: head = %+v, %v, want nothing recorded", v, verr)
	}
	if n := advisoryLocks(t, db); n != 0 {
		t.Errorf("%d advisory locks held after the cancelled run", n)
	}

	fast := migrator(t, db, "live_cancel_history", []migrate.Migration{{Version: 1, Name: "slow", Up: "SELECT 1", Transactional: true}}, migrate.Options{})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := fast.Up(ctx2); err != nil {
		t.Fatalf("Up after cancellation: %v (the lock was not released)", err)
	}
}

const (
	lowerHistory = "live_ms_lower_history"
	upperHistory = "live_ms_upper_history"
)

var lowerMigrations = []migrate.Migration{
	{Version: 1, Name: "table", Up: "CREATE TABLE live_ms_lower (id integer PRIMARY KEY, name text NOT NULL)", Down: "DROP TABLE live_ms_lower", Transactional: true},
	{Version: 2, Name: "column", Up: "ALTER TABLE live_ms_lower ADD COLUMN note text", Down: "ALTER TABLE live_ms_lower DROP COLUMN note", Transactional: true},
}

var rehearsal = migrate.Migration{
	Version: 3, Name: "rehearsal",
	Up:            "CREATE INDEX live_ms_lower_rehearsal ON live_ms_lower (name)",
	Down:          "DROP INDEX live_ms_lower_rehearsal",
	Transactional: true,
}

func lowerSet(rehearsed bool) migrate.Set {
	ms := slices.Clone(lowerMigrations)
	if rehearsed {
		ms = append(ms, rehearsal)
	}
	return migrate.Set{Name: "lower", Table: lowerHistory, Migrations: ms}
}

var upperSet = migrate.Set{Name: "upper", Table: upperHistory, Migrations: []migrate.Migration{
	{Version: 1, Name: "table", Up: "CREATE TABLE live_ms_upper (id integer PRIMARY KEY, lower_id integer NOT NULL REFERENCES live_ms_lower (id))", Down: "DROP TABLE live_ms_upper", Transactional: true},
	{Version: 2, Name: "index", Up: "CREATE INDEX live_ms_upper_lower_ix ON live_ms_upper (lower_id)", Down: "DROP INDEX live_ms_upper_lower_ix", Transactional: true},
}}

func multiset(rehearsed bool) []migrate.Set { return []migrate.Set{lowerSet(rehearsed), upperSet} }

func scratchMultiset(t testing.TB, db *sqlate.DB) {
	t.Helper()
	scratch(t, db, upperHistory, "live_ms_upper")
	scratch(t, db, lowerHistory, "live_ms_lower")
	scratch(t, db, "live_gate_history", "live_gate")
	scratch(t, db, "live_probe_history")
}

func count(t testing.TB, db *sqlate.DB, q string, args ...any) int {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer func() { _ = rows.Close() }()
	var n int
	if !rows.Next() || rows.Scan(&n) != nil {
		t.Fatalf("%s: no row: %v", q, rows.Err())
	}
	return n
}

func text(t testing.TB, db *sqlate.DB, q string, args ...any) string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer func() { _ = rows.Close() }()
	var s string
	if !rows.Next() || rows.Scan(&s) != nil {
		t.Fatalf("%s: no row: %v", q, rows.Err())
	}
	return s
}

func relationExists(t testing.TB, db *sqlate.DB, name string) bool {
	t.Helper()
	return count(t, db, "SELECT COUNT(*) FROM pg_class WHERE oid = to_regclass($1)", name) == 1
}

func assertRelations(t testing.TB, db *sqlate.DB, want bool, names ...string) {
	t.Helper()
	for _, r := range names {
		if got := relationExists(t, db, r); got != want {
			t.Errorf("%s exists = %v, want %v", r, got, want)
		}
	}
}

func assertClean(t testing.TB, m *migrate.Migrator, versions ...int) {
	t.Helper()
	sets, err := m.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(sets) != len(versions) {
		t.Fatalf("Status returned %d sets, want %d", len(sets), len(versions))
	}
	for i, s := range sets {
		if s.Version != versions[i] || s.Latest != versions[i] || len(s.Pending) != 0 || s.Dirty {
			t.Errorf("set %s status = %+v, want version %d, nothing pending, clean", s.Name, s, versions[i])
		}
	}
}

func assertReverted(t testing.TB, m *migrate.Migrator, migrations ...int) {
	t.Helper()
	sets, err := m.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(sets) != len(migrations) {
		t.Fatalf("Status returned %d sets, want %d", len(sets), len(migrations))
	}
	for i, s := range sets {
		if s.Version != 0 || len(s.Pending) != migrations[i] || s.Dirty {
			t.Errorf("set %s status = %+v, want version 0 with all %d migrations pending, clean", s.Name, s, migrations[i])
		}
	}
}

func seedRows(t testing.TB, db *sqlate.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		"INSERT INTO live_ms_lower (id, name) VALUES (1, 'a')",
		"INSERT INTO live_ms_upper (id, lower_id) VALUES (1, 1)",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func assertDependentObjects(t testing.TB, what string, err error, set string) {
	t.Helper()
	var se *migrate.SetError
	if !errors.As(err, &se) || se.Set != set {
		t.Fatalf("%s = %v, want a SetError naming %q", what, err, set)
	}
	if !errors.Is(err, sqlate.ErrDependentObjects) || sqlState(err) != "2BP01" {
		t.Errorf("%s = %v, want ErrDependentObjects over SQLSTATE 2BP01", what, err)
	}
}

func TestLive_MultisetFreshReplay(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	scratchMultiset(t, db)
	m := migratorOver(t, db, multiset(false), migrate.Options{})
	objects := []string{"live_ms_lower", "live_ms_upper", lowerHistory, upperHistory}

	before, err := m.Status(ctx)
	if err != nil {
		t.Fatalf("Status on an empty database: %v", err)
	}
	if before[0].Version != 0 || len(before[0].Pending) != 2 || before[1].Version != 0 || len(before[1].Pending) != 2 {
		t.Errorf("Status on an empty database = %+v, want everything pending", before)
	}
	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	assertClean(t, m, 2, 2)
	assertRelations(t, db, true, objects...)
	if err := m.Verify(ctx); err != nil {
		t.Errorf("Verify after Up: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO live_ms_lower (id, name) VALUES (1, 'stale')"); err != nil {
		t.Fatal(err)
	}

	if err := m.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	assertRelations(t, db, false, objects...)
	if after, err := m.Status(ctx); err != nil || after[0].Version != 0 || after[1].Version != 0 {
		t.Errorf("Status after Reset = %+v, %v; want version 0 for both sets", after, err)
	}

	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up after Reset: %v", err)
	}
	assertClean(t, m, 2, 2)
	assertRelations(t, db, true, objects...)
	if n := count(t, db, "SELECT COUNT(*) FROM live_ms_lower"); n != 0 {
		t.Errorf("after the replay live_ms_lower holds %d rows, want 0: the replay did not start from zero", n)
	}
}

func TestLive_MultisetUpgradeAfterRestart(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	scratchMultiset(t, db)
	installed := migratorOver(t, db, multiset(false), migrate.Options{})
	if err := installed.Up(ctx); err != nil {
		t.Fatalf("Up at version 2: %v", err)
	}
	assertClean(t, installed, 2, 2)
	if exists, _ := indexValid(t, db, "live_ms_lower_rehearsal"); exists {
		t.Fatal("the rehearsal index exists before the upgrade")
	}
	seedRows(t, db)
	rowsBefore := count(t, db, "SELECT (SELECT COUNT(*) FROM live_ms_lower) + (SELECT COUNT(*) FROM live_ms_upper)")
	appliedAt := text(t, db, "SELECT applied_at::text FROM "+lowerHistory+" WHERE version = 1")

	restarted := live(t)
	upgraded := migratorOver(t, restarted, multiset(true), migrate.Options{})
	status, err := upgraded.Status(ctx)
	if err != nil {
		t.Fatalf("Status before the upgrade: %v", err)
	}
	if l := status[0]; l.Version != 2 || l.Latest != 3 || len(l.Pending) != 1 || l.Pending[0].Version != 3 || l.Pending[0].Name != rehearsal.Name {
		t.Errorf("lower status before the upgrade = %+v, want version 2 of 3 with the rehearsal pending", l)
	}
	if err := upgraded.Up(ctx); err != nil {
		t.Fatalf("Up after the restart: %v", err)
	}
	assertClean(t, upgraded, 3, 2)
	if exists, valid := indexValid(t, restarted, "live_ms_lower_rehearsal"); !exists || !valid {
		t.Errorf("rehearsal index exists=%v valid=%v", exists, valid)
	}
	if n := count(t, restarted, "SELECT COUNT(*) FROM "+lowerHistory); n != 3 {
		t.Errorf("lower history holds %d rows after the upgrade, want 3", n)
	}
	if got := text(t, restarted, "SELECT applied_at::text FROM "+lowerHistory+" WHERE version = 1"); got != appliedAt {
		t.Errorf("version 1 applied_at = %s after the upgrade, was %s: the upgrade re-applied it", got, appliedAt)
	}
	if rowsAfter := count(t, restarted, "SELECT (SELECT COUNT(*) FROM live_ms_lower) + (SELECT COUNT(*) FROM live_ms_upper)"); rowsAfter != rowsBefore {
		t.Errorf("%d rows after the upgrade, %d before", rowsAfter, rowsBefore)
	}
	if err := installed.Verify(ctx); !errors.Is(err, migrate.ErrUnknownVersion) {
		t.Errorf("the installed binary's Verify over the upgraded history = %v, want ErrUnknownVersion", err)
	}
}

func TestLive_MultisetResetOrderAcrossForeignKeys(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	scratchMultiset(t, db)
	sets := multiset(true)
	m := migratorOver(t, db, sets, migrate.Options{})
	objects := []string{"live_ms_lower", "live_ms_upper", lowerHistory, upperHistory}

	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	seedRows(t, db)
	if err := m.Reset(ctx); err != nil {
		t.Fatalf("Reset with a row under the foreign key: %v", err)
	}
	assertRelations(t, db, false, objects...)

	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up again: %v", err)
	}
	seedRows(t, db)
	lower, ok := m.Layer("lower")
	if !ok {
		t.Fatal("no lower layer")
	}
	if err := lower.Down(ctx, 1); !errors.Is(err, migrate.ErrAboveApplied) {
		t.Errorf("Down of the lower layer under the upper = %v, want ErrAboveApplied", err)
	}
	if v, _ := lower.Version(ctx); v.Version != 3 {
		t.Errorf("lower head after the library-level refusal = %+v, want 3 untouched", v)
	}

	wrongOrder := migratorOver(t, db, []migrate.Set{sets[1], sets[0]}, migrate.Options{})
	assertDependentObjects(t, "Reset in the wrong order", wrongOrder.Reset(ctx), "lower")
	lowerAlone := migratorOver(t, db, sets[:1], migrate.Options{})
	assertDependentObjects(t, "Down of the lower set alone", lowerAlone.Down(ctx, 3), "lower")
	assertRelations(t, db, true, objects...)
	if v, err := lower.Version(ctx); err != nil || v.Version != 1 || v.Dirty {
		t.Errorf("lower head after the refused reverts = %+v, %v; want clean at 1 (index and column reverted, the table drop refused)", v, err)
	}
	if exists, _ := indexValid(t, db, "live_ms_lower_rehearsal"); exists {
		t.Error("the rehearsal index survived the refused revert")
	}
	if n := count(t, db, "SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'live_ms_lower' AND column_name = 'note'"); n != 0 {
		t.Error("the note column survived the refused revert")
	}
	if err := m.Reset(ctx); err != nil {
		t.Fatalf("Reset in the right order after the refusals: %v", err)
	}
	assertRelations(t, db, false, objects...)
}

const gateName = "live.gate"

var gateSet = migrate.Set{
	Name:  "gate",
	Table: "live_gate_history",
	Migrations: []migrate.Migration{{
		Version:       1,
		Name:          "gate",
		Up:            "CREATE TABLE live_gate (id int);\nSELECT pg_advisory_xact_lock(hashtext('" + gateName + "'));",
		Down:          "DROP TABLE live_gate",
		Transactional: true,
	}},
}

func holdGate(t *testing.T, db *sqlate.DB) func() {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext($1))", gateName); err != nil {
		t.Fatalf("hold the gate: %v", err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtext($1))", gateName); err != nil {
				t.Errorf("release the gate: %v", err)
			}
			_ = conn.Close()
		})
	}
	t.Cleanup(release)
	return release
}

func waitForWaiters(t *testing.T, db *sqlate.DB, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		got := count(t, db, "SELECT COUNT(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'")
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d backends wait on a lock, want %d", got, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForQueue(t *testing.T, db *sqlate.DB) {
	t.Helper()
	const (
		scope   = "SELECT COUNT(*) FROM pg_stat_activity WHERE datname = current_database() AND "
		gated   = scope + "wait_event_type = 'Lock' AND query LIKE '%pg_advisory_xact_lock%'"
		queued  = scope + "((query = 'SELECT pg_try_advisory_lock(hashtext($1))' AND query_start > $1::timestamptz) OR (query = 'SELECT pg_advisory_lock(hashtext($1))' AND wait_event_type = 'Lock'))"
		timeout = 20 * time.Second
	)
	since := text(t, db, "SELECT clock_timestamp()::text")
	deadline := time.Now().Add(timeout)
	for count(t, db, gated) < 1 || count(t, db, queued, since) < 1 {
		if time.Now().After(deadline) {
			t.Fatal("no starter waits on the gate with another queued for the migrator's lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startBoth(ctx context.Context, a, b *migrate.Migrator) (errA, errB error) {
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(2)
	run := func(m *migrate.Migrator, out *error) {
		defer done.Done()
		start.Wait()
		*out = m.Up(ctx)
	}
	go run(a, &errA)
	go run(b, &errB)
	start.Done()
	done.Wait()
	return errA, errB
}

func TestLive_MultisetStartersSerialize(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	scratchMultiset(t, db)
	sets := append([]migrate.Set{gateSet}, multiset(false)...)
	histories := map[string]int{"live_gate_history": 1, lowerHistory: 2, upperHistory: 2}

	release := holdGate(t, db)
	a := migratorOver(t, db, sets, migrate.Options{})
	b := migratorOver(t, db, sets, migrate.Options{})
	var errA, errB error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		errA, errB = startBoth(ctx, a, b)
	}()
	waitForQueue(t, db)
	release()
	<-finished
	if errA != nil || errB != nil {
		t.Fatalf("serialized starters: a = %v, b = %v; want both to succeed", errA, errB)
	}
	assertClean(t, a, 1, 2, 2)
	for table, want := range histories {
		if n := count(t, db, "SELECT COUNT(*) FROM "+table); n != want {
			t.Errorf("%s holds %d rows, want %d: a migration was applied more or less than once", table, n, want)
		}
	}

	layers := a.Layers()
	for _, layer := range slices.Backward(layers) {
		if err := layer.Down(ctx, len(layer.Migrations())); err != nil {
			t.Fatalf("Down of %s: %v", layer.Name(), err)
		}
	}
	assertReverted(t, a, 1, 2, 2)
	assertRelations(t, db, true, "live_gate_history", lowerHistory, upperHistory)
	assertRelations(t, db, false, "live_gate", "live_ms_lower", "live_ms_upper")

	release = holdGate(t, db)
	a = migratorOver(t, db, sets, migrate.Options{Unlocked: true})
	b = migratorOver(t, db, sets, migrate.Options{Unlocked: true})
	finished = make(chan struct{})
	go func() {
		defer close(finished)
		errA, errB = startBoth(ctx, a, b)
	}()
	waitForWaiters(t, db, 2)
	release()
	<-finished
	if (errA == nil) == (errB == nil) {
		t.Fatalf("unlocked starters: a = %v, b = %v; want exactly one to fail", errA, errB)
	}
	failed := errors.Join(errA, errB)
	if st := sqlState(failed); st != "42P07" && st != "23505" {
		t.Errorf("the unlocked loser failed with %v, want a duplicate object (SQLSTATE 42P07 or 23505)", failed)
	}
	t.Logf("the unlocked loser failed with SQLSTATE %s: %v", sqlState(failed), failed)
	for table, want := range histories {
		if n := count(t, db, "SELECT COUNT(*) FROM "+table); n != want {
			t.Errorf("after the control %s holds %d rows, want %d", table, n, want)
		}
	}
}

func TestLive_MultisetDirtyRefusal(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	scratchMultiset(t, db)
	probe := migrate.Set{Name: "probe", Table: "live_probe_history", Migrations: []migrate.Migration{{
		Version: 1, Name: "fails_midway",
		Up:   "CREATE INDEX CONCURRENTLY live_probe_ix ON no_such_table (x)",
		Down: "DROP INDEX live_probe_ix",
	}}}
	first := migratorOver(t, db, []migrate.Set{probe}, migrate.Options{})
	err := first.Up(ctx)
	var de *migrate.DirtyError
	if !errors.As(err, &de) || de.Version != 1 || sqlState(err) != "42P01" {
		t.Fatalf("Up of the failing migration = %v, want a DirtyError at version 1 over 42P01", err)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM live_probe_history WHERE version = 1 AND dirty"); n != 1 {
		t.Fatalf("the history holds %d dirty rows at version 1, want 1", n)
	}

	m := migratorOver(t, db, []migrate.Set{lowerSet(false), probe}, migrate.Options{})
	lower, _ := m.Layer("lower")
	verbs := map[string]func(context.Context) error{
		"Up":             m.Up,
		"Down(1)":        func(ctx context.Context) error { return m.Down(ctx, 1) },
		"Steps(1)":       func(ctx context.Context) error { return m.Steps(ctx, 1) },
		"Steps(-1)":      func(ctx context.Context) error { return m.Steps(ctx, -1) },
		"Reset":          m.Reset,
		"lower.Steps(1)": func(ctx context.Context) error { return lower.Steps(ctx, 1) },
		"lower.Down(1)":  func(ctx context.Context) error { return lower.Down(ctx, 1) },
	}
	for name, op := range verbs {
		err := op(ctx)
		var se *migrate.SetError
		if !errors.Is(err, migrate.ErrDirty) || !errors.As(err, &se) || se.Set != "probe" || !errors.As(err, &de) || de.Version != 1 {
			t.Errorf("%s over a dirty set = %v, want ErrDirty naming set probe at version 1", name, err)
		}
	}
	var se *migrate.SetError
	if err := m.Verify(ctx); !errors.Is(err, migrate.ErrPending) || !errors.As(err, &se) || se.Set != "lower" {
		t.Errorf("Verify over a pending lower set and a dirty probe = %v, want ErrPending naming set lower first", err)
	}
	probeLayer, _ := m.Layer("probe")
	if err := probeLayer.Verify(ctx); !errors.Is(err, migrate.ErrDirty) || !errors.As(err, &se) || se.Set != "probe" || !errors.As(err, &de) || de.Version != 1 {
		t.Errorf("the probe layer's Verify = %v, want ErrDirty naming set probe at version 1", err)
	}
	assertRelations(t, db, false, "live_ms_lower")
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status[0].Version != 0 || status[0].Dirty {
		t.Errorf("lower status = %+v, want version 0 clean", status[0])
	}
	if p := status[1]; !p.Dirty || p.Version != 1 || p.Name != "probe" {
		t.Errorf("probe status = %+v, want version 1 dirty", p)
	}

	if err := m.Force(ctx, 0); err != nil {
		t.Fatalf("Force on probe, the top set: %v", err)
	}
	if status, _ := m.Status(ctx); status[1].Dirty || status[1].Version != 0 {
		t.Errorf("probe status after Force = %+v, want version 0 clean", status[1])
	}
	if err := m.Reset(ctx); err != nil {
		t.Fatalf("Reset after Force: %v", err)
	}
	assertRelations(t, db, false, "live_probe_history", lowerHistory)
	err = m.Up(ctx)
	if !errors.As(err, &se) || se.Set != "probe" || !errors.As(err, &de) || de.Version != 1 {
		t.Errorf("Up after the reset = %v, want the probe's DirtyError again", err)
	}
	assertRelations(t, db, true, "live_ms_lower")
}

func TestLive_HistoryExistsIsSchemaQualified(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	const history = "live_decoy_history"
	scratch(t, db, history)
	_, _ = db.ExecContext(ctx, "DROP SCHEMA IF EXISTS live_decoy CASCADE")
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA live_decoy"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DROP SCHEMA IF EXISTS live_decoy CASCADE") })
	if _, err := db.ExecContext(ctx, "CREATE TABLE live_decoy."+history+" (x int)"); err != nil {
		t.Fatal(err)
	}

	if n := count(t, db, migrate.StandardCatalog{}.HistoryExists("$1"), history); n != 1 {
		t.Fatalf("the standard existence check counts %d, want 1: the decoy is not visible and this proof proves nothing", n)
	}
	if n := count(t, db, postgres.Dialect{}.HistoryExists("$1"), history); n != 0 {
		t.Fatalf("the qualified existence check counts %d, want 0", n)
	}

	set := []migrate.Migration{{Version: 1, Name: "noop", Up: "SELECT 1", Down: "SELECT 1", Transactional: true}}
	m := migrator(t, db, history, set, migrate.Options{})
	if v, err := m.Version(ctx); err != nil || v != (migrate.Version{}) {
		t.Errorf("Version beside the decoy = %+v, %v; want the zero version and no error", v, err)
	}
	if s, err := m.Status(ctx); err != nil || s[0].Version != 0 || len(s[0].Pending) != 1 {
		t.Errorf("Status beside the decoy = %+v, %v; want version 0 with 1 pending", s, err)
	}
	var se *migrate.SetError
	if err := m.Verify(ctx); !errors.Is(err, migrate.ErrPending) || !errors.As(err, &se) || se.Set != "live" {
		t.Errorf("Verify beside the decoy = %v, want ErrPending naming the set", err)
	}
	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up beside the decoy: %v", err)
	}
	if v, err := m.Version(ctx); err != nil || v.Version != 1 || v.Dirty {
		t.Errorf("Version after Up = %+v, %v; want clean at 1", v, err)
	}
	if !relationExists(t, db, "public."+history) {
		t.Error("Up did not create the history in the current schema")
	}
	if n := count(t, db, "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'live_decoy' AND table_name = $1 AND column_name = 'version'", history); n != 0 {
		t.Error("the decoy gained the history's columns")
	}
}

func TestLive_HistoryUpgradeKeepsEachInstant(t *testing.T) {
	ctx := context.Background()
	db := liveIn(t, "Asia/Tokyo")
	const history = "live_upgrade_history"
	scratch(t, db, history)
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+history+" (version integer PRIMARY KEY, name text NOT NULL, applied_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP, dirty boolean NOT NULL DEFAULT FALSE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO "+history+" (version, name, applied_at, dirty) VALUES (1, 'one', '2026-01-15 12:30:00.5', FALSE)"); err != nil {
		t.Fatal(err)
	}
	typeOf := "SELECT data_type FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = 'applied_at'"
	if got := text(t, db, typeOf, history); got != "timestamp without time zone" {
		t.Fatalf("old-style applied_at is %q; the proof starts from the wrong shape", got)
	}

	var logged bytes.Buffer
	set := []migrate.Migration{
		{Version: 1, Name: "one", Up: "SELECT 1", Transactional: true},
		{Version: 2, Name: "two", Up: "SELECT 2", Transactional: true},
	}
	m := migrator(t, db, history, set, migrate.Options{Logger: slog.New(slog.NewTextHandler(&logged, nil))})
	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up over the old-style history: %v", err)
	}
	if got := text(t, db, typeOf, history); got != "timestamp with time zone" {
		t.Errorf("applied_at after Up is %q, want timestamp with time zone", got)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM "+history+" WHERE version = 1 AND applied_at = CAST('2026-01-15 12:30:00.5+00' AS timestamp with time zone)"); n != 1 {
		t.Errorf("version 1's applied_at = %s, want the stored wall clock read as UTC",
			text(t, db, "SELECT CAST(applied_at AS text) FROM "+history+" WHERE version = 1"))
	}
	if n := count(t, db, "SELECT COUNT(*) FROM "+history+" WHERE version = 2 AND applied_at BETWEEN CURRENT_TIMESTAMP - INTERVAL '1 minute' AND CURRENT_TIMESTAMP"); n != 1 {
		t.Errorf("version 2's applied_at = %s, want the default's current instant",
			text(t, db, "SELECT CAST(applied_at AS text) FROM "+history+" WHERE version = 2"))
	}

	if err := m.Up(ctx); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if n := strings.Count(logged.String(), "history upgraded"); n != 1 {
		t.Errorf("history upgraded %d times over two runs, want once:\n%s", n, logged.String())
	}
	if n := count(t, db, "SELECT COUNT(*) FROM "+history+" WHERE version = 1 AND applied_at = CAST('2026-01-15 12:30:00.5+00' AS timestamp with time zone)"); n != 1 {
		t.Error("the second Up moved version 1's instant")
	}
}

func TestLive_HistoryIsCreatedTimeZoneAware(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	const history = "live_aware_history"
	scratch(t, db, history)
	m := migrator(t, db, history, []migrate.Migration{{Version: 1, Name: "one", Up: "SELECT 1", Transactional: true}}, migrate.Options{})
	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if got := text(t, db, "SELECT data_type FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = 'applied_at'", history); got != "timestamp with time zone" {
		t.Errorf("applied_at is %q, want timestamp with time zone", got)
	}
	if n := count(t, db, postgres.Dialect{}.HistoryOutdated("$1"), history); n != 0 {
		t.Errorf("HistoryOutdated counts %d for a current table, want 0", n)
	}
}

func TestLive_ConcurrentIndexBuildDoesNotDeadlockTheWaiter(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	scratch(t, db, "live_cic_history", "live_cic")
	set := []migrate.Migration{
		{
			Version:       1,
			Name:          "table",
			Up:            "CREATE TABLE live_cic (x int);\nSELECT pg_advisory_xact_lock(hashtext('" + gateName + "'));",
			Down:          "DROP TABLE live_cic",
			Transactional: true,
		},
		{Version: 2, Name: "index", Up: "CREATE INDEX CONCURRENTLY live_cic_x ON live_cic (x)", Down: "DROP INDEX CONCURRENTLY live_cic_x"},
	}
	release := holdGate(t, db)
	a := migrator(t, db, "live_cic_history", set, migrate.Options{})
	b := migrator(t, db, "live_cic_history", set, migrate.Options{})
	var errA, errB error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		errA, errB = startBoth(ctx, a, b)
	}()
	waitForQueue(t, db)
	release()
	<-finished
	if errA != nil || errB != nil {
		t.Fatalf("starters over a concurrent index build: a = %v (SQLSTATE %q), b = %v (SQLSTATE %q); want both to succeed",
			errA, sqlState(errA), errB, sqlState(errB))
	}
	assertAppliedOnce(t, db, "live_cic_history", len(set))
	if exists, valid := indexValid(t, db, "live_cic_x"); !exists || !valid {
		t.Errorf("index live_cic_x exists=%v valid=%v, want one valid index", exists, valid)
	}
}
