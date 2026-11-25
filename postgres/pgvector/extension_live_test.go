//go:build integration

package pgvector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"
	"github.com/standards-lab/sqlate/postgres/pgtest"
	"github.com/standards-lab/sqlate/postgres/pgvector"
)

const dsnEnv = "SQLATE_DSN"

var declared = []migrate.Extension{{Name: pgvector.Extension}}

func vectorSet(migrations ...migrate.Migration) []migrate.Set {
	return []migrate.Set{{Name: "vec", Table: "vec_history", Migrations: migrations}}
}

var docTable = migrate.Migration{
	Version: 1, Name: "doc",
	Up:   "CREATE TABLE doc (id integer PRIMARY KEY, embedding vector(3))",
	Down: "DROP TABLE doc", Transactional: true,
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

func installedVersion(t testing.TB, db *sqlate.DB) string {
	t.Helper()
	return text(t, db, "SELECT COALESCE((SELECT extversion FROM pg_extension WHERE extname = $1), '')", pgvector.Extension)
}

func imageDefaultVersion(t testing.TB, db *sqlate.DB) string {
	t.Helper()
	v := text(t, db, "SELECT COALESCE((SELECT default_version FROM pg_available_extensions WHERE name = $1), '')", pgvector.Extension)
	if v == "" {
		t.Fatalf("the image does not offer %s", pgvector.Extension)
	}
	return v
}

func migratorOver(t testing.TB, db *sqlate.DB, sets []migrate.Set, opts migrate.Options) *migrate.Migrator {
	t.Helper()
	m, err := migrate.New(db, sets, opts)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func statusOf(t testing.TB, m *migrate.Migrator) migrate.ExtensionStatus {
	t.Helper()
	got, err := m.Extensions(context.Background())
	if err != nil {
		t.Fatalf("Extensions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Extensions = %+v, want one status", got)
	}
	return got[0]
}

func TestLive_DeclaredVectorInstallsAtTheImageDefault(t *testing.T) {
	ctx := context.Background()
	opts := migrate.Options{Extensions: declared}
	d := pgtest.MigratedWith(t, dsnEnv, "vec", opts, vectorSet(docTable)...)
	db := d.Session()
	def := imageDefaultVersion(t, db)
	if got := installedVersion(t, db); got != def {
		t.Fatalf("vector after MigratedWith is at %q, want the image default %q", got, def)
	}

	m := migratorOver(t, db, vectorSet(docTable), opts)
	want := migrate.ExtensionStatus{Name: pgvector.Extension, Installed: def, Default: def}
	if got := statusOf(t, m); got != want {
		t.Errorf("Extensions = %+v, want %+v", got, want)
	}
	if err := m.Verify(ctx); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestLive_RemovedVectorWaitsForItsDependentColumn(t *testing.T) {
	ctx := context.Background()
	d := pgtest.MigratedWith(t, dsnEnv, "vec", migrate.Options{Extensions: declared}, vectorSet(docTable)...)
	db := d.Session()
	def := imageDefaultVersion(t, db)
	removed := migrate.Options{Extensions: []migrate.Extension{{Name: pgvector.Extension, Removed: true}}}

	blocked := migratorOver(t, db, vectorSet(docTable), removed)
	err := blocked.Up(ctx)
	extErr, ok := errors.AsType[*migrate.ExtensionError](err)
	if !ok || extErr.Extension != pgvector.Extension {
		t.Fatalf("Up removing vector under a dependent column = %v, want an *ExtensionError naming vector", err)
	}
	if !errors.Is(err, sqlate.ErrDependentObjects) {
		t.Errorf("Up = %v, want it over sqlate.ErrDependentObjects", err)
	}
	if got := installedVersion(t, db); got != def {
		t.Errorf("vector after the refused removal is at %q, want %q still", got, def)
	}
	if got := statusOf(t, blocked); !got.Pending || !got.Removed {
		t.Errorf("Extensions after the refused removal = %+v, want a pending removal", got)
	}

	dropColumn := migrate.Migration{
		Version: 2, Name: "drop embedding",
		Up:   "ALTER TABLE doc DROP COLUMN embedding",
		Down: "ALTER TABLE doc ADD COLUMN embedding vector(3)", Transactional: true,
	}
	m := migratorOver(t, db, vectorSet(docTable, dropColumn), removed)
	if err := m.Up(ctx); err != nil {
		t.Fatalf("Up after the column's drop: %v", err)
	}
	if got := installedVersion(t, db); got != "" {
		t.Errorf("vector after Up is at %q, want it dropped", got)
	}
	if err := m.Verify(ctx); err != nil {
		t.Errorf("Verify after the removal: %v", err)
	}
}
