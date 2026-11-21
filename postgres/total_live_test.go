//go:build integration

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

const continuedPageOffset = -1

var onlyX = []query.Filter{{Field: "grp", Op: query.OpEq, Value: "x"}}

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
	case offset == continuedPageOffset && n > total:
		return fmt.Sprintf("%d items past a total of %d", n, total)
	case offset != continuedPageOffset && offset+n > total:
		return fmt.Sprintf("items end at %d past a total of %d", offset+n, total)
	case offset != continuedPageOffset && c.More != (offset+n < total):
		return fmt.Sprintf("More = %v with items ending at %d of a total of %d", c.More, offset+n, total)
	}
	return ""
}

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

func countX(t testing.TB, s sqlate.Session, table string) int {
	t.Helper()
	n, err := readX(s, table)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func readX(s sqlate.Session, table string) (int, error) {
	rows, err := s.QueryContext(context.Background(), "SELECT COUNT(*) FROM "+table+" WHERE grp = 'x'")
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var n int
	if !rows.Next() {
		return 0, fmt.Errorf("no count: %v", rows.Err())
	}
	err = rows.Scan(&n)
	return n, err
}

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
		offset := continuedPageOffset
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

	const rounds = 6
	agreed := 0
	for round := range rounds {
		c, err := items.List(ctx, r, d, query.Page{Number: 1, Size: 1000})
		if err != nil {
			t.Fatal(err)
		}
		if now := countX(t, db, table); now == c.Total {
			t.Errorf("counted round %d: the count is still %d; the racer did not land", round, now)
		}
		if d := disagreement(c, c.Total, 0, 1000); d != "" || len(c.Items) != c.Total || c.More {
			t.Errorf("counted round %d: %d items, More %v, total %d: %s", round, len(c.Items), c.More, c.Total, d)
			continue
		}
		agreed++
	}
	t.Logf("counted rounds agreed in %d of %d", agreed, rounds)

	disagreed := 0
	for round := range rounds {
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

type stressResult struct {
	lists, continues, writes, violations int64
	first                                []string
}

func stress(t *testing.T, db *sqlate.DB, table string, read func(violate func(string)) (continued int)) stressResult {
	t.Helper()
	ctx := context.Background()
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
			for time.Now().Before(deadline) && !t.Failed() {
				continues.Add(int64(read(violate)))
				lists.Add(1)
			}
		})
	}
	wg.Wait()
	return stressResult{lists: lists.Load(), continues: continues.Load(), writes: writes.Load(), violations: violations.Load(), first: first}
}

func TestLive_TotalAgreesUnderConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	db := live(t)
	const size = 25
	items := liveTotal(t, db, "live_total_stress", 400)
	d := query.Directives{Sort: []query.Sort{{Field: "n"}}, Filters: onlyX}
	res := stress(t, db, "live_total_stress", func(violate func(string)) int {
		number := 1 + rand.IntN(20)
		c, err := items.List(ctx, db, d, query.Page{Number: number, Size: size})
		if err != nil {
			t.Errorf("List: %v", err)
			return 0
		}
		empty := query.NoTotal
		if number == 1 {
			empty = 0
		}
		if len(c.Items) == 0 && c.Total != empty {
			violate(fmt.Sprintf("empty page %d: Total = %d", number, c.Total))
		}
		if msg := disagreement(c, c.Total, (number-1)*size, size); msg != "" {
			violate(fmt.Sprintf("page %d: %s", number, msg))
		}
		hops := 0
		for ; hops < 3 && c.Next != ""; hops++ {
			if c, err = items.Continue(ctx, db, d, c.Next, size); err != nil {
				t.Errorf("Continue: %v", err)
				return hops
			}
			if len(c.Items) == 0 && c.Total != query.NoTotal {
				violate(fmt.Sprintf("empty Continue page: Total = %d", c.Total))
			}
			if msg := disagreement(c, c.Total, continuedPageOffset, size); msg != "" {
				violate("Continue: " + msg)
			}
		}
		return hops
	})
	t.Logf("stress: %d List and %d Continue reads against %d committed write batches, %d violations", res.lists, res.continues, res.writes, res.violations)
	if res.violations != 0 {
		t.Errorf("%d pages disagreed with their totals; the first:\n%s", res.violations, strings.Join(res.first, "\n"))
	}
	if res.lists == 0 || res.continues == 0 || res.writes == 0 {
		t.Errorf("the stress ran no reads or writes of some kind")
	}

	const control = "live_total_stress_control"
	plain := liveTotal(t, db, control, 400)
	res = stress(t, db, control, func(violate func(string)) int {
		total, err := readX(db, control)
		if err != nil {
			t.Errorf("control count: %v", err)
			return 0
		}
		c, err := plain.List(ctx, db, query.Directives{Sort: d.Sort, Filters: onlyX, Total: query.TotalNone}, query.Page{Number: 1, Size: 1000})
		if err != nil {
			t.Errorf("control List: %v", err)
			return 0
		}
		if msg := disagreement(c, total, 0, 1000); msg != "" {
			violate(fmt.Sprintf("counted %d, page has %d: %s", total, len(c.Items), msg))
		}
		return 0
	})
	t.Logf("stress control: %d two-statement reads against %d committed write batches, %d violations", res.lists, res.writes, res.violations)
	if res.violations == 0 {
		t.Errorf("the two-statement control never disagreed under the stress; the stress cannot catch the form it guards against")
	}
}

type capture struct {
	*sqlate.DB
	text string
	args []any
}

func (c *capture) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	c.text, c.args = q, args
	return c.DB.QueryContext(ctx, q, args...)
}

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

func walk(n planNode, visit func(planNode)) {
	visit(n)
	for _, c := range n.Plans {
		walk(c, visit)
	}
}

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
