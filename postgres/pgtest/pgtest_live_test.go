//go:build integration

// The live proof of pgtest runs against the PostgreSQL named by SQLATE_DSN
// (the compose stack: `mise run db:up`, then `mise run integration`) and
// skips when it is unset, the module's live-test convention.
package pgtest_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/standards-lab/sqlate/migrate"
	"github.com/standards-lab/sqlate/postgres/pgtest"
)

const env = "SQLATE_DSN"

// Proof: Migrated creates <prefix>_test_<hex>, applies the set to it, and
// the database is dropped when the test that made it ends, even with a
// session of another pool still open on it.
func TestLive_MigratedDroppedWithSessionOpen(t *testing.T) {
	admin := os.Getenv(env)
	if admin == "" {
		t.Skip(env + " not set")
	}
	ctx := context.Background()
	set := migrate.Set{Name: "pgtest", Migrations: []migrate.Migration{{
		Version:       1,
		Name:          "widgets",
		Up:            "CREATE TABLE widgets (id integer PRIMARY KEY)",
		Down:          "DROP TABLE widgets",
		Transactional: true,
	}}}

	var name string
	var held *sql.DB
	var conn *sql.Conn
	t.Cleanup(func() {
		if conn != nil {
			_ = conn.Close()
		}
		if held != nil {
			_ = held.Close()
		}
	})
	t.Run("database", func(t *testing.T) {
		d := pgtest.Migrated(t, env, "pgtest", set)
		u, err := url.Parse(d.DSN)
		if err != nil {
			t.Fatal(err)
		}
		name = strings.TrimPrefix(u.Path, "/")
		if !strings.HasPrefix(name, "pgtest_test_") {
			t.Errorf("database name %q, want the pgtest_test_ prefix", name)
		}
		if !exists(t, admin, name) {
			t.Fatalf("database %s does not exist after Migrated", name)
		}
		if _, err := d.Session().ExecContext(ctx, "INSERT INTO widgets (id) VALUES (1)"); err != nil {
			t.Fatalf("the migrated table is not usable: %v", err)
		}

		// A second pool, outside pgtest's cleanup, holds a session open on
		// the database past the end of the subtest.
		held, err = sql.Open("pgx", d.DSN)
		if err != nil {
			t.Fatal(err)
		}
		conn, err = held.Conn(ctx)
		if err != nil {
			t.Fatalf("hold a session: %v", err)
		}
		if err := conn.PingContext(ctx); err != nil {
			t.Fatalf("ping the held session: %v", err)
		}
	})
	if name == "" || conn == nil {
		t.Fatal("the subtest did not create a database and hold a session on it")
	}
	if exists(t, admin, name) {
		t.Errorf("database %s still exists after its test's cleanup", name)
	}
	// The drop ended the held session rather than waiting for it.
	if err := conn.PingContext(ctx); err == nil {
		t.Error("the held session survived the drop")
	}
}

// exists reports whether the server admin connects to has the database.
func exists(t testing.TB, admin, name string) bool {
	t.Helper()
	db, err := sql.Open("pgx", admin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var found bool
	err = db.QueryRowContext(context.Background(),
		"SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&found)
	if err != nil {
		t.Fatalf("read the database list: %v", err)
	}
	return found
}
