package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	"github.com/standards-lab/sqlate/postgres"
)

// timeout bounds each statement the package runs against the server, and
// each attempt of the drop separately.
const timeout = 30 * time.Second

// Database is one test's throwaway database: the pool over it and the DSN
// that names it.
type Database struct {
	Pool *sql.DB
	DSN  string
}

// Session wraps the pool under postgres.Dialect.
func (d *Database) Session() *sqlate.DB {
	return sqlate.Wrap(d.Pool, postgres.Dialect{})
}

// Create creates a uniquely named, empty database, <prefix>_test_<hex>, on
// the server whose admin DSN the environment variable env holds, and
// returns a pinged pool over it and the admin DSN pointed at it. An unset
// env fails the test: the caller's integration tier states that the server
// is expected. When the test ends the pool is closed and the database
// dropped, ending any session still open on it.
func Create(t testing.TB, env, prefix string) *Database {
	t.Helper()
	admin := os.Getenv(env)
	if admin == "" {
		t.Fatalf("%s is not set; run with the PostgreSQL it names up", env)
	}
	name := scratch(t, admin, prefix)
	dsn := databaseDSN(t, admin, name)
	return &Database{Pool: pool(t, dsn), DSN: dsn}
}

// Migrated is Create with sets applied, in order, through migrate.New and
// Up under postgres.Dialect.
func Migrated(t testing.TB, env, prefix string, sets ...migrate.Set) *Database {
	t.Helper()
	d := Create(t, env, prefix)
	m, err := migrate.New(d.Session(), sets, migrate.Options{})
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := m.Up(ctx); err != nil {
		t.Fatalf("migrate Up: %v", err)
	}
	return d
}

// scratch creates the database <prefix>_test_<hex> over a connection to
// admin and returns its name, registering its drop as a cleanup. The name
// is quoted, so it reaches the server exactly as the DSN spells it.
func scratch(t testing.TB, admin, prefix string) string {
	t.Helper()
	db, err := sql.Open("pgx", admin)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	name := prefix + "_test_" + suffix(t)
	ident := pgx.Identifier{name}.Sanitize()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if _, err := db.ExecContext(ctx, "CREATE DATABASE "+ident); err != nil {
		// A create that timed out can still finish on the server, so the
		// database is dropped if it exists, on a context of its own: ctx
		// may be the one that expired.
		dctx, dcancel := context.WithTimeout(context.Background(), timeout)
		_, _ = db.ExecContext(dctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
		dcancel()
		_ = db.Close()
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		// WITH (FORCE) ends the database's other sessions, and the server
		// can still report one closing, so the drop retries, each attempt
		// with a deadline of its own so one stall does not use up the rest.
		var err error
		for range 4 {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			_, err = db.ExecContext(ctx, "DROP DATABASE "+ident+" WITH (FORCE)")
			cancel()
			if err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		_ = db.Close()
	})
	return name
}

// pool opens a pool on dsn and pings it. Its cleanup is registered after
// the drop's, so it runs first: the pool closes before the database drops.
func pool(t testing.TB, dsn string) *sql.DB {
	t.Helper()
	p, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := p.PingContext(ctx); err != nil {
		t.Fatalf("ping test database: %v", err)
	}
	return p
}

// databaseDSN returns admin with its database replaced by name.
func databaseDSN(t testing.TB, admin, name string) string {
	t.Helper()
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("parse admin DSN: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// suffix returns eight random hex characters, so parallel tests and
// packages never pick the same database name.
func suffix(t testing.TB) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random suffix: %v", err)
	}
	return hex.EncodeToString(b[:])
}
