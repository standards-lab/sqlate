//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/postgres"
)

func live(t testing.TB) *sqlate.DB {
	t.Helper()
	return liveIn(t, "")
}

func liveIn(t testing.TB, zone string) *sqlate.DB {
	t.Helper()
	dsn := os.Getenv("SQLATE_DSN")
	if dsn == "" {
		t.Skip("SQLATE_DSN not set")
	}
	if zone != "" {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "timezone=" + zone
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

func sqlState(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code
	}
	return ""
}

func TestLive_ServerVersion(t *testing.T) {
	db := live(t)
	rows, err := db.QueryContext(context.Background(), postgres.Dialect{}.ServerVersion())
	if err != nil {
		t.Fatalf("ServerVersion: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var version string
	if !rows.Next() || rows.Scan(&version) != nil {
		t.Fatalf("ServerVersion read no row: %v", rows.Err())
	}
	if !strings.HasPrefix(version, "PostgreSQL ") {
		t.Errorf("ServerVersion read %q, want the engine's version string", version)
	}
}
