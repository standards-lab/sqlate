package sqltest_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"
)

// flag scripts the one boolean row a try-lock or unlock statement reads.
func flag(b bool) sqltest.Response {
	return sqltest.Response{Columns: []string{"ok"}, Rows: [][]driver.Value{{b}}}
}

// pinned opens a pool over the script and pins one connection, as a lock
// protocol does.
func pinned(t *testing.T, responses ...sqltest.Response) (*sql.Conn, *sqltest.Recorder) {
	t.Helper()
	pool, rec := sqltest.Open(t, responses...)
	conn, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, rec
}

func TestLockingDialect_IsATryLocker(t *testing.T) {
	var d sqlate.Dialect = sqltest.LockingDialect{}
	if _, ok := d.(sqlate.TryLocker); !ok {
		t.Fatal("LockingDialect does not assert as sqlate.TryLocker")
	}
	if _, ok := d.(sqlate.Locker); !ok {
		t.Fatal("LockingDialect does not assert as sqlate.Locker")
	}
}

func TestLockingDialect_TryLockReportsTheScriptedOutcome(t *testing.T) {
	ctx := context.Background()
	conn, rec := pinned(t, flag(true), flag(false))
	d := sqltest.LockingDialect{}

	if got, err := d.TryLock(ctx, conn, "app.sweep"); err != nil || !got {
		t.Errorf("TryLock with true scripted = %v, %v; want true, nil", got, err)
	}
	if got, err := d.TryLock(ctx, conn, "app.sweep"); err != nil || got {
		t.Errorf("TryLock with false scripted = %v, %v; want false, nil", got, err)
	}
	calls := rec.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want two", calls)
	}
	for _, c := range calls {
		if c.Op != sqltest.OpQuery || c.SQL != "SELECT try_lock($1)" || len(c.Args) != 1 || c.Args[0] != "app.sweep" {
			t.Errorf("call = %+v, want the try-lock query under app.sweep", c)
		}
	}
}

func TestLockingDialect_TryLockPassesAScriptedErrorThrough(t *testing.T) {
	boom := errors.New("boom")
	conn, _ := pinned(t, sqltest.Response{Err: boom})
	got, err := sqltest.LockingDialect{}.TryLock(context.Background(), conn, "app.sweep")
	if got || !errors.Is(err, boom) {
		t.Errorf("TryLock = %v, %v; want false, the scripted error", got, err)
	}
}

func TestLockingDialect_LockAndUnlockRunTheirStatements(t *testing.T) {
	ctx := context.Background()
	conn, rec := pinned(t, sqltest.Response{}, flag(true), flag(false))
	d := sqltest.LockingDialect{}

	if err := d.Lock(ctx, conn, "app.sweep"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := d.Unlock(ctx, conn, "app.sweep"); err != nil {
		t.Fatalf("Unlock of a held lock: %v", err)
	}
	if err := d.Unlock(ctx, conn, "app.sweep"); !errors.Is(err, sqltest.ErrLockNotHeld) {
		t.Errorf("Unlock with false scripted = %v, want ErrLockNotHeld", err)
	}
	want := []sqltest.Call{
		{Op: sqltest.OpExec, SQL: "SELECT lock($1)"},
		{Op: sqltest.OpQuery, SQL: "SELECT unlock($1)"},
		{Op: sqltest.OpQuery, SQL: "SELECT unlock($1)"},
	}
	calls := rec.Calls()
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %d", calls, len(want))
	}
	for i, c := range calls {
		if c.Op != want[i].Op || c.SQL != want[i].SQL || len(c.Args) != 1 || c.Args[0] != "app.sweep" {
			t.Errorf("call %d = %+v, want %s %q under app.sweep", i, c, want[i].Op, want[i].SQL)
		}
	}
	if rec.Pending() != 0 || rec.RowsLeaked() != 0 {
		t.Errorf("pending = %d, leaked = %d", rec.Pending(), rec.RowsLeaked())
	}
}
