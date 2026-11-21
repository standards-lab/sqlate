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

func pin(t *testing.T, db *sqlate.DB) *sql.Conn {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func tryLock(t *testing.T, conn *sql.Conn, name string) bool {
	t.Helper()
	got, err := postgres.Dialect{}.TryLock(context.Background(), conn, name)
	if err != nil {
		t.Fatalf("TryLock %s: %v", name, err)
	}
	return got
}

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
		if err := holder.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
			t.Fatalf("Raw = %v, want the discard", err)
		}
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
