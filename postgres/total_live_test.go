//go:build integration

// Live proofs for the exact total against the compose PostgreSQL: the count
// is a window in the page's own statement, so the total and the page read one
// snapshot and agree even while writes land between the statement and the
// caller; the old two-statement form, a count and then the page, is shown to
// disagree under the same writes; the agreement holds under concurrent
// writers on the pool; and the two total modes plan as the design says, the
// counted page scanning its base once under one window and the uncounted one
// keeping the key's index order. `mise run integration`.
package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"
)

type totalRow struct {
	ID, Grp string
	N       int64
}

// liveTotal creates a proof's table, keyed by a uuid primary key (and so
// indexed on the key), with rows 'x' rows and a fifth as many 'y' rows the
// filters exclude, n spread over 0..999, and drops it at cleanup. It returns
// the table's projection under the engine's patterns.
func liveTotal(t testing.TB, db *sqlate.DB, table string, rows int) query.Projection[totalRow] {
	t.Helper()
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table)
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id uuid PRIMARY KEY DEFAULT uuidv7(), grp text NOT NULL, n integer NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table) })
	seed := fmt.Sprintf("INSERT INTO %s (grp, n) SELECT CASE WHEN g %% 6 = 0 THEN 'y' ELSE 'x' END, (random() * 999)::int FROM generate_series(1, %d) g", table, rows+rows/5)
	if _, err := db.ExecContext(ctx, seed); err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		"sql/items.sql": {Data: []byte("--| tier: standard\n--| key: id\n--| field: id uuid not null\n--| field: grp text not null\n--| field: n integer not null\nSELECT id, grp, n FROM " + table)},
	}
	stmts := query.MustCatalog(postgres.Patterns()).MustCompile(files, "sql", db.Dialect())
	items := stmts.Statement("items").Project(query.Scanner[totalRow]())
	if err := query.Verify(ctx, db, stmts, items); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return items
}

// onlyX is the proofs' filter: the 'x' rows, which every write touches.
var onlyX = []query.Filter{{Field: "grp", Op: query.OpEq, Value: "x"}}

// disagreement reports how a page with items disagrees with the total it is
// read against, or "" when they agree. offset is the page's offset, or -1
// for a Continue page, whose offset is unknown: then only the bounds a
// position does not enter are checked.
func disagreement(c query.Collection[totalRow], total, offset, size int) string {
	n := len(c.Items)
	for _, it := range c.Items {
		if it.Grp != "x" {
			return fmt.Sprintf("an item outside the filter: %+v", it)
		}
	}
	switch {
	case n == 0:
		return ""
	case total < 0:
		return fmt.Sprintf("%d items and no total", n)
	case c.More && n != size:
		return fmt.Sprintf("More with %d items on a page of %d", n, size)
	case offset < 0 && n > total:
		return fmt.Sprintf("%d items past a total of %d", n, total)
	case offset >= 0 && offset+n > total:
		return fmt.Sprintf("items end at %d past a total of %d", offset+n, total)
	case offset >= 0 && c.More != (offset+n < total):
		return fmt.Sprintf("More = %v with items ending at %d of a total of %d", c.More, offset+n, total)
	}
	return ""
}

// racer is a session that, each time a query returns, commits a write to the
// filtered rows on a connection of its own: an 'x' row inserted, then the
// lowest 'x' row deleted, alternately. The write lands after the engine has
// begun the query's statement, and so taken its snapshot, and before the
// caller reads a row of it: between a total being computed and the caller
// seeing it.
type racer struct {
	*sqlate.DB
	t      testing.TB
	conn   *sql.Conn
	table  string
	writes int
}

func (r *racer) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	rows, err := r.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	write := fmt.Sprintf("INSERT INTO %s (grp, n) VALUES ('x', 500)", r.table)
	if r.writes%2 == 1 {
		write = fmt.Sprintf("DELETE FROM %s WHERE id = (SELECT id FROM %[1]s WHERE grp = 'x' ORDER BY n, id LIMIT 1)", r.table)
	}
	if _, err := r.conn.ExecContext(ctx, write); err != nil {
		_ = rows.Close()
		r.t.Fatalf("racing write: %v", err)
	}
	r.writes++
	return rows, nil
}

// countX reads the filtered rows' count now, on the pool.
func countX(t testing.TB, s sqlate.Session, table string) int {
	t.Helper()
	rows, err := s.QueryContext(context.Background(), "SELECT COUNT(*) FROM "+table+" WHERE grp = 'x'")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var n int
	if !rows.Next() {
		t.Fatalf("no count: %v", rows.Err())
	}
	if err := rows.Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Proof: the counted total agrees with its page when a write commits between
// the page's statement and the caller reading it. Every read runs through a
// racer, and each page is shown to have raced a write (the count now differs
// from the page's total) and still to agree with its total: a page with More
// is full, and an offset page's items end at or before the total, before it
// exactly when More. The control emulates the old design under the same
// racer, an explicit COUNT(*) statement and then an uncounted page, and the
// two disagree every round.
func TestLive_TotalAgreesUnderRacingWrites(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	const table = "live_total_race"
	items := liveTotal(t, db, table, 20)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	r := &racer{DB: db, t: t, conn: conn, table: table}
	byN := []query.Sort{{Field: "n"}}
	const size = 6

	pages, raced := 0, 0
	check := func(label string, c query.Collection[totalRow], offset int) {
		t.Helper()
		if len(c.Items) == 0 {
			return
		}
		pages++
		if d := disagreement(c, c.Total, offset, size); d != "" {
			t.Errorf("%s: %s (%+v)", label, d, c)
		}
		if now := countX(t, db, table); now != c.Total {
			raced++
		}
	}

	d := query.Directives{Sort: byN, Filters: onlyX}
	for number := 1; ; number++ {
		c, err := items.List(ctx, r, d, query.Page{Number: number, Size: size})
		if err != nil {
			t.Fatalf("page %d: %v", number, err)
		}
		if len(c.Items) == 0 {
			if c.Total != query.NoTotal {
				t.Errorf("page %d past the end: Total = %d, want NoTotal", number, c.Total)
			}
			break
		}
		check(fmt.Sprintf("offset page %d", number), c, (number-1)*size)
		if number == 10 {
			t.Fatal("the offset walk did not end")
		}
	}
	c, err := items.List(ctx, r, d, query.Page{Number: 1, Size: size})
	for page := 1; ; page++ {
		if err != nil {
			t.Fatalf("cursor page %d: %v", page, err)
		}
		offset := -1 // a Continue page's offset is unknown
		if page == 1 {
			offset = 0
		}
		check(fmt.Sprintf("cursor page %d", page), c, offset)
		if c.Next == "" {
			break
		}
		if page == 10 {
			t.Fatal("the cursor walk did not end")
		}
		c, err = items.Continue(ctx, r, d, c.Next, size)
	}
	if raced != pages {
		t.Errorf("%d of %d pages raced a write; the racer did not land between the statement and the caller", raced, pages)
	}
	t.Logf("counted: %d pages, each raced by a write (%d writes), 0 disagreements", pages, r.writes)

	// The control: the total from its own statement, then the page. Each
	// round's page reads every filtered row, so it agrees with the count
	// only when nothing changed between the two statements.
	const rounds = 6
	disagreed := 0
	for round := range rounds {
		// Each round runs two queries, so the write racing the count
		// would be the same every round; stepping the racer on odd
		// rounds puts an insert between the two in some rounds and a
		// delete in others.
		r.writes += round % 2
		total := countX(t, r, table)
		c, err := items.List(ctx, r, query.Directives{Sort: byN, Filters: onlyX, Total: query.TotalNone}, query.Page{Number: 1, Size: 1000})
		if err != nil {
			t.Fatal(err)
		}
		if c.Total != query.NoTotal {
			t.Fatalf("TotalNone page carries Total = %d", c.Total)
		}
		if d := disagreement(c, total, 0, 1000); d != "" {
			disagreed++
			t.Logf("control round %d: counted %d, page has %d: %s", round, total, len(c.Items), d)
		}
	}
	if disagreed != rounds {
		t.Errorf("the two-statement control disagreed in %d of %d rounds, want every round", disagreed, rounds)
	}
	t.Logf("control: two statements disagreed in %d of %d rounds", disagreed, rounds)
}

// Proof: under concurrent writers on the pool, every counted page a reader
// sees agrees with its own total. Two writers insert and delete batches of
// 50 filtered rows for two seconds while four readers run List at a random
// page and Continue from it, outside any transaction; no page may disagree.
func TestLive_TotalAgreesUnderConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	const table = "live_total_stress"
	items := liveTotal(t, db, table, 400)
	const size = 25
	deadline := time.Now().Add(2 * time.Second)

	var lists, continues, writes, violations atomic.Int64
	var mu sync.Mutex
	var first []string
	violate := func(msg string) {
		if violations.Add(1) <= 5 {
			mu.Lock()
			first = append(first, msg)
			mu.Unlock()
		}
	}

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			// SKIP LOCKED keeps the two writers' deletes from waiting on,
			// and so deadlocking with, each other's rows.
			insert := fmt.Sprintf("INSERT INTO %s (grp, n) SELECT 'x', (random() * 999)::int FROM generate_series(1, 50)", table)
			del := fmt.Sprintf("DELETE FROM %s WHERE id IN (SELECT id FROM %[1]s WHERE grp = 'x' ORDER BY random() LIMIT 50 FOR UPDATE SKIP LOCKED)", table)
			for i := 0; time.Now().Before(deadline); i++ {
				stmt := insert
				if i%2 == 1 {
					stmt = del
				}
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Errorf("writer: %v", err)
					return
				}
				writes.Add(1)
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			d := query.Directives{Sort: []query.Sort{{Field: "n"}}, Filters: onlyX}
			for time.Now().Before(deadline) {
				number := 1 + rand.IntN(20)
				c, err := items.List(ctx, db, d, query.Page{Number: number, Size: size})
				if err != nil {
					t.Errorf("List: %v", err)
					return
				}
				lists.Add(1)
				empty := query.NoTotal // an empty later page carries no count
				if number == 1 {
					empty = 0
				}
				if len(c.Items) == 0 && c.Total != empty {
					violate(fmt.Sprintf("empty page %d: Total = %d", number, c.Total))
				}
				if msg := disagreement(c, c.Total, (number-1)*size, size); msg != "" {
					violate(fmt.Sprintf("page %d: %s", number, msg))
				}
				for hop := 0; hop < 3 && c.Next != ""; hop++ {
					if c, err = items.Continue(ctx, db, d, c.Next, size); err != nil {
						t.Errorf("Continue: %v", err)
						return
					}
					continues.Add(1)
					if len(c.Items) == 0 && c.Total != query.NoTotal {
						violate(fmt.Sprintf("empty Continue page: Total = %d", c.Total))
					}
					if msg := disagreement(c, c.Total, -1, size); msg != "" {
						violate("Continue: " + msg)
					}
				}
			}
		})
	}
	wg.Wait()
	t.Logf("stress: %d List and %d Continue reads against %d committed write batches, %d violations", lists.Load(), continues.Load(), writes.Load(), violations.Load())
	if n := violations.Load(); n != 0 {
		t.Errorf("%d pages disagreed with their totals; the first:\n%s", n, strings.Join(first, "\n"))
	}
	if lists.Load() == 0 || continues.Load() == 0 || writes.Load() == 0 {
		t.Errorf("the stress ran no reads or writes of some kind")
	}
}

// capture is a session that keeps the last query's text and arguments.
type capture struct {
	*sqlate.DB
	text string
	args []any
}

func (c *capture) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	c.text, c.args = q, args
	return c.DB.QueryContext(ctx, q, args...)
}

// planNode is one node of EXPLAIN (FORMAT JSON)'s plan tree.
type planNode struct {
	NodeType   string     `json:"Node Type"`
	Relation   string     `json:"Relation Name"`
	Plans      []planNode `json:"Plans"`
	SharedHit  int64      `json:"Shared Hit Blocks"`
	SharedRead int64      `json:"Shared Read Blocks"`
}

type explained struct {
	Plan          planNode `json:"Plan"`
	ExecutionTime float64  `json:"Execution Time"`
}

// explain runs EXPLAIN with the given options over a captured statement and
// its arguments.
func explain(t testing.TB, db *sqlate.DB, options string, c *capture) explained {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN ("+options+") "+c.text, c.args...)
	if err != nil {
		t.Fatalf("EXPLAIN %s: %v", c.text, err)
	}
	defer func() { _ = rows.Close() }()
	var raw []byte
	if !rows.Next() {
		t.Fatalf("EXPLAIN returned no plan: %v", rows.Err())
	}
	if err := rows.Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var out []explained
	if err := json.Unmarshal(raw, &out); err != nil || len(out) != 1 {
		t.Fatalf("EXPLAIN JSON %s: %v", raw, err)
	}
	return out[0]
}

// walk visits a plan tree depth first.
func walk(n planNode, visit func(planNode)) {
	visit(n)
	for _, c := range n.Plans {
		walk(c, visit)
	}
}

// planShape lists a plan's node types depth first, a scan with its relation.
func planShape(n planNode) string {
	var parts []string
	walk(n, func(n planNode) {
		if n.Relation != "" {
			parts = append(parts, n.NodeType+" on "+n.Relation)
		} else {
			parts = append(parts, n.NodeType)
		}
	})
	return strings.Join(parts, " > ")
}

// Proof: the plans of the two total modes over 10k rows sorted by the
// indexed key. The counted page has exactly one WindowAgg and scans its base
// relation once, so the count costs no second pass; the uncounted page has no
// WindowAgg and no Sort beneath its Limit, reading the key's index in order.
// Each plan's execution time and shared buffers are logged, not asserted.
func TestLive_TotalPlans(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	const table = "live_total_plan"
	items := liveTotal(t, db, table, 10000)
	if _, err := db.ExecContext(ctx, "ANALYZE "+table); err != nil {
		t.Fatal(err)
	}
	byKey := []query.Sort{{Field: "id"}}

	counted := &capture{DB: db}
	c, err := items.List(ctx, counted, query.Directives{Sort: byKey}, query.Page{Number: 1, Size: 20})
	if err != nil || len(c.Items) != 20 || c.Total != 12000 {
		t.Fatalf("counted List = %d items, Total %d, %v; want 20 of 12000", len(c.Items), c.Total, err)
	}
	uncounted := &capture{DB: db}
	c, err = items.List(ctx, uncounted, query.Directives{Sort: byKey, Total: query.TotalNone}, query.Page{Number: 1, Size: 20})
	if err != nil || len(c.Items) != 20 || c.Total != query.NoTotal {
		t.Fatalf("uncounted List = %d items, Total %d, %v; want 20 and NoTotal", len(c.Items), c.Total, err)
	}
	if !strings.Contains(counted.text, "COUNT(*) OVER ()") || strings.Contains(uncounted.text, "COUNT(*)") {
		t.Fatalf("the captured texts are not the two modes:\n%s\n%s", counted.text, uncounted.text)
	}

	plan := explain(t, db, "FORMAT JSON", counted).Plan
	windows, scans := 0, 0
	walk(plan, func(n planNode) {
		if n.NodeType == "WindowAgg" {
			windows++
		}
		if n.Relation == table {
			scans++
		}
	})
	if windows != 1 || scans != 1 {
		t.Errorf("counted plan has %d WindowAgg and %d scans of %s, want one each: %s", windows, scans, table, planShape(plan))
	}
	t.Logf("TotalExact plan: %s", planShape(plan))

	plan = explain(t, db, "FORMAT JSON", uncounted).Plan
	if plan.NodeType != "Limit" {
		t.Errorf("uncounted plan's top node is %s, want Limit: %s", plan.NodeType, planShape(plan))
	}
	walk(plan, func(n planNode) {
		if n.NodeType == "WindowAgg" || n.NodeType == "Sort" || n.NodeType == "Incremental Sort" {
			t.Errorf("uncounted plan has a %s node: %s", n.NodeType, planShape(plan))
		}
	})
	t.Logf("TotalNone plan: %s", planShape(plan))

	for _, m := range []struct {
		name string
		c    *capture
	}{{"TotalExact", counted}, {"TotalNone", uncounted}} {
		e := explain(t, db, "ANALYZE, BUFFERS, FORMAT JSON", m.c)
		t.Logf("%s: execution %.3f ms, shared buffers hit %d read %d", m.name, e.ExecutionTime, e.Plan.SharedHit, e.Plan.SharedRead)
	}
}
