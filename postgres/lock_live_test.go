//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/postgres"
)

// pin pins one connection of db, closed at cleanup unless the test closed
// it first.
func pin(t *testing.T, db *sqlate.DB) *sql.Conn {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// tryLock is Dialect.TryLock that fails the test on an error.
func tryLock(t *testing.T, conn *sql.Conn, name string) bool {
	t.Helper()
	got, err := postgres.Dialect{}.TryLock(context.Background(), conn, name)
	if err != nil {
		t.Fatalf("TryLock %s: %v", name, err)
	}
	return got
}

// Proof: TryLock shares Lock's key space and session scope. A name one
// session holds, through Lock or TryLock, is refused to TryLock on another
// without waiting, and is granted again once the holder unlocks it or its
// connection closes.
func TestLive_TryLock(t *testing.T) {
	ctx := context.Background()
	d := postgres.Dialect{}

	t.Run("refused while another session holds it through Lock", func(t *testing.T) {
		db := live(t)
		const name = "live.trylock_lock"
		holder, other := pin(t, db), pin(t, db)
		if err := d.Lock(ctx, holder, name); err != nil {
			t.Fatalf("Lock: %v", err)
		}
		if tryLock(t, other, name) {
			t.Fatal("TryLock on another session took a name held through Lock")
		}
		if err := d.Unlock(ctx, holder, name); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
		if !tryLock(t, other, name) {
			t.Fatal("TryLock refused a name released by Unlock")
		}
		if err := d.Unlock(ctx, other, name); err != nil {
			t.Fatalf("Unlock of the lock TryLock took: %v", err)
		}
	})

	t.Run("refused while another session holds it through TryLock", func(t *testing.T) {
		db := live(t)
		const name = "live.trylock_trylock"
		holder, other := pin(t, db), pin(t, db)
		if !tryLock(t, holder, name) {
			t.Fatal("TryLock refused a free name")
		}
		if tryLock(t, other, name) {
			t.Fatal("TryLock on another session took a name held through TryLock")
		}
		if err := d.Unlock(ctx, holder, name); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
		if !tryLock(t, other, name) {
			t.Fatal("TryLock refused a name released by Unlock")
		}
		if err := d.Unlock(ctx, other, name); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
	})

	t.Run("granted once the holding connection closes", func(t *testing.T) {
		db := live(t)
		const name = "live.trylock_close"
		holder, other := pin(t, db), pin(t, db)
		if !tryLock(t, holder, name) {
			t.Fatal("TryLock refused a free name")
		}
		if tryLock(t, other, name) {
			t.Fatal("TryLock on another session took a held name")
		}
		// Closing a pinned conn returns it to the pool with its session, and
		// the lock, intact; a Raw func answering driver.ErrBadConn makes
		// database/sql close the driver connection, which ends the session.
		if err := holder.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
			t.Fatalf("Raw = %v, want the discard", err)
		}
		// The server ends the backend after the socket closes, not with it,
		// so the grant is awaited.
		deadline := time.Now().Add(5 * time.Second)
		for !tryLock(t, other, name) {
			if time.Now().After(deadline) {
				t.Fatal("TryLock refused a name whose holding connection closed")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := d.Unlock(ctx, other, name); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
	})
}
