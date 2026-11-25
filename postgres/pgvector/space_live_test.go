//go:build integration

package pgvector_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate/migrate"
	"github.com/standards-lab/sqlate/postgres/pgtest"
	"github.com/standards-lab/sqlate/postgres/pgvector"
)

const rowsPerSpace = 200

type space struct {
	id     int
	dims   int
	metric pgvector.Metric
	index  string
}

func spacesPerMetric() []space {
	var spaces []space
	for i, m := range pgvector.Metrics() {
		id := i + 1
		spaces = append(spaces, space{
			id:     id,
			dims:   i + 3,
			metric: m,
			index:  fmt.Sprintf("item_space%d_%s", id, strings.ReplaceAll(m.Name, " ", "_")),
		})
	}
	return spaces
}

func (s space) createIndex() string {
	return fmt.Sprintf("CREATE INDEX %s ON item USING hnsw ((embedding::vector(%d)) %s) WHERE space = %d",
		s.index, s.dims, s.metric.OpClass, s.id)
}

func (s space) seed() string {
	return fmt.Sprintf("INSERT INTO item (space, embedding) "+
		"SELECT %d, CAST(ARRAY(SELECT sin(g * k) FROM generate_series(1, %d) AS k) AS vector) "+
		"FROM generate_series(1, %d) AS g", s.id, s.dims, rowsPerSpace)
}

func (s space) nearestWithCast() string {
	return fmt.Sprintf("SELECT id FROM item WHERE space = %d ORDER BY embedding::vector(%d) %s $1 LIMIT 5",
		s.id, s.dims, s.metric.Operator)
}

func (s space) nearestWithoutCast() string {
	return fmt.Sprintf("SELECT id FROM item WHERE space = %d ORDER BY embedding %s $1 LIMIT 5",
		s.id, s.metric.Operator)
}

func (s space) probe() string {
	return vectorLiteral(s.dims)
}

func vectorLiteral(dims int) string {
	parts := make([]string, dims)
	for i := range parts {
		parts[i] = fmt.Sprintf("%d", i+1)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func perSpaceSet(spaces []space) []migrate.Set {
	migrations := []migrate.Migration{{
		Version: 1, Name: "item",
		Up:   "CREATE TABLE item (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, space integer NOT NULL, embedding vector NOT NULL)",
		Down: "DROP TABLE item", Transactional: true,
	}}
	for _, s := range spaces {
		migrations = append(migrations,
			migrate.Migration{
				Version: len(migrations) + 1, Name: s.index,
				Up: s.createIndex(), Down: "DROP INDEX " + s.index, Transactional: true,
			},
			migrate.Migration{
				Version: len(migrations) + 2, Name: s.index + " rows",
				Up: s.seed(), Down: fmt.Sprintf("DELETE FROM item WHERE space = %d", s.id), Transactional: true,
			})
	}
	return vectorSet(migrations...)
}

func plan(t testing.TB, pool *sql.DB, query, probe string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("SET LOCAL enable_seqscan: %v", err)
	}
	rows, err := tx.QueryContext(ctx, "EXPLAIN "+query, probe)
	if err != nil {
		t.Fatalf("EXPLAIN %s: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("EXPLAIN %s: %v", query, err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("EXPLAIN %s: %v", query, err)
	}
	return strings.Join(lines, "\n")
}

func TestLive_QueryRepeatingTheCastScansItsSpaceIndex(t *testing.T) {
	spaces := spacesPerMetric()
	d := pgtest.MigratedWith(t, dsnEnv, "vec", migrate.Options{Extensions: declared}, perSpaceSet(spaces)...)
	for _, s := range spaces {
		t.Run(s.metric.Name, func(t *testing.T) {
			got := plan(t, d.Pool, s.nearestWithCast(), s.probe())
			if !strings.Contains(got, "Index Scan using "+s.index+" ") {
				t.Errorf("plan of %s does not scan %s:\n%s", s.nearestWithCast(), s.index, got)
			}
			rows, err := d.Pool.QueryContext(context.Background(), s.nearestWithCast(), s.probe())
			if err != nil {
				t.Fatalf("%s: %v", s.nearestWithCast(), err)
			}
			defer func() { _ = rows.Close() }()
			n := 0
			for rows.Next() {
				n++
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("%s: %v", s.nearestWithCast(), err)
			}
			if n != 5 {
				t.Errorf("%s returned %d rows, want 5", s.nearestWithCast(), n)
			}
		})
	}
}

func TestLive_QueryWithoutTheCastMissesEveryIndex(t *testing.T) {
	spaces := spacesPerMetric()
	d := pgtest.MigratedWith(t, dsnEnv, "vec", migrate.Options{Extensions: declared}, perSpaceSet(spaces)...)
	for _, s := range spaces {
		t.Run(s.metric.Name, func(t *testing.T) {
			got := plan(t, d.Pool, s.nearestWithoutCast(), s.probe())
			for _, other := range spaces {
				if strings.Contains(got, other.index) {
					t.Errorf("plan of %s uses %s:\n%s", s.nearestWithoutCast(), other.index, got)
				}
			}
		})
	}
}

func TestLive_InsertOfTheWrongDimensionIntoAnIndexedSpaceFails(t *testing.T) {
	spaces := spacesPerMetric()
	d := pgtest.MigratedWith(t, dsnEnv, "vec", migrate.Options{Extensions: declared}, perSpaceSet(spaces)...)
	for _, s := range spaces {
		t.Run(s.metric.Name, func(t *testing.T) {
			_, err := d.Pool.ExecContext(context.Background(),
				"INSERT INTO item (space, embedding) VALUES ($1, $2)", s.id, vectorLiteral(s.dims+1))
			want := fmt.Sprintf("expected %d dimensions", s.dims)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("insert of %d dimensions into space %d = %v, want an error containing %q", s.dims+1, s.id, err, want)
			}
		})
	}
}
