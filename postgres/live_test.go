//go:build integration

// The live-engine acceptance proofs run against the PostgreSQL named by
// SQLATE_DSN — the compose stack, `mise run db-up` then
// `mise run test-integration` — and skip when it is unset. They are not
// part of the unit tier: each is a proof the engine alone can give,
// demonstrated in the session that lands the claim.
package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/postgres"
)

// live opens a session over the live database, or skips.
func live(t testing.TB) *sqlate.DB {
	t.Helper()
	dsn := os.Getenv("SQLATE_DSN")
	if dsn == "" {
		t.Skip("SQLATE_DSN not set")
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := pool.PingContext(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return sqlate.Wrap(pool, postgres.Dialect{})
}

// sqlState reads the SQLSTATE of a driver error, or "".
func sqlState(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code
	}
	return ""
}
