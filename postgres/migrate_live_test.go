//go:build integration

// The live-engine acceptance proofs for migrate: the cases a mature library
// has met, run against the live PostgreSQL named by SQLATE_DSN. Each
// proof owns its object names and drops them on exit, so the proofs run in
// any order and survive an aborted run. `mise run test-integration`.
package postgres_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	"github.com/standards-lab/sqlate/postgres"
)

// scratch drops the named objects now and at cleanup: the history table
// and every table or index a proof creates.
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
	opts.Table = history
	m, err := migrate.New(db, set, opts)
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

// Proof: non-transactional DDL. CREATE INDEX CONCURRENTLY refuses a
// transaction block (SQLSTATE 25001); the "-- transaction: none" opt-out
// runs it under autocommit on the pinned connection, and the history row
// goes dirty → clean around it.
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

// Proof: dirty state and force, with the orphan PostgreSQL leaves behind. A
// unique index built CONCURRENTLY over duplicate rows fails after the
// catalog entry exists, so the index remains INVALID: the history row is
// dirty, every run refuses, Verify reports it, and the repair is drop the
// orphan, Force the previous version, fix the data, and re-run.
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
		t.Fatalf("Up = %v, want DirtyError{3} carrying the unique violation", err)
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

	// The repair, as an operator would do it.
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

// slowSet is a set whose first migration holds its transaction open long
// enough for a second starter to collide with it.
var slowSet = []migrate.Migration{
	{Version: 1, Name: "slow", Up: "SELECT pg_sleep(0.4)", Down: "SELECT 1", Transactional: true},
	{Version: 2, Name: "table", Up: "CREATE TABLE live_race (x int)", Down: "DROP TABLE live_race", Transactional: true},
}

// Proof: concurrent starters in one process. Four migrators over one pool
// start together; the lock serializes them, every Up succeeds, and the
// history and the schema each show one application.
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

// Proof: the unlocked negative. The same race without the lock: every
// starter reads an empty history and applies; the collision surfaces as
// duplicate_table (42P07) or a duplicate history row (23505) in all but one.
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

// Proof: concurrent starters across processes. The test binary re-executes
// itself four times, each child a process with its own pool running Up on
// the same history; the advisory lock serializes them across sessions.
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

// TestLive_Helper is the child process of the cross-process proof.
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

// Proof: the cancelled-context run. A migration sleeping past the deadline
// is cancelled mid-statement: Up returns the cancellation and nothing else
// (pgx discards the connection, so the rollback and the unlock on it cannot
// succeed and are not reported), nothing is recorded, no advisory lock
// survives, and the next run proceeds at once.
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
		t.Errorf("the cancellation carried connection noise: %v", err)
	}
	if v, verr := m.Version(context.Background()); verr != nil || v.Version != 0 {
		t.Errorf("after cancellation: head = %+v, %v, want nothing recorded", v, verr)
	}
	if n := advisoryLocks(t, db); n != 0 {
		t.Errorf("%d advisory locks held after the cancelled run", n)
	}

	// The lock is free: a fresh run over a fast set completes well within
	// the time the cancelled sleep would still be holding it.
	fast := migrator(t, db, "live_cancel_history", []migrate.Migration{{Version: 1, Name: "slow", Up: "SELECT 1", Transactional: true}}, migrate.Options{})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := fast.Up(ctx2); err != nil {
		t.Fatalf("Up after cancellation: %v (the lock was not released)", err)
	}
}
