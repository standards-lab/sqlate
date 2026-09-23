//go:build integration

// Live proof for the returning command against the compose PostgreSQL: the
// same files compiled twice, once under the dialect, whose RETURNING makes
// each command one statement, and once under a wrapper that hides the
// capability, where the command and then its read run in a transaction.
// Both forms return the row an immediate read returns, in every outcome of
// a plain and a guarded command, and differ only in the statements the
// engine receives. `mise run integration`.
package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/postgres"
	"github.com/standards-lab/sqlate/query"
)

// returningFS holds the proof's pattern namespace, live, and its statements:
// the read, and three returning commands naming it.
var returningFS = fstest.MapFS{
	"patterns/item_columns.sql": {Data: []byte("--| tier: standard\ni.id, i.status, i.size, i.version, i.created_at, i.updated_at")},
	"sql/item_by_id.sql":        {Data: []byte("--| tier: standard\nSELECT {{> live.item_columns}} FROM item i WHERE i.id = {{id}}")},
	"sql/create_item.sql":       {Data: []byte("--| tier: standard\n--| returning: item_by_id\nINSERT INTO item (id, status, size) VALUES ({{id}}, {{status}}, {{size}})")},
	"sql/claim_item.sql":        {Data: []byte("--| tier: standard\n--| returning: item_by_id\nUPDATE item\nSET status = 'claimed', {{> sql.guard_set}}\nWHERE {{> sql.guard_where}} AND status = 'open'")},
	"sql/retire_item.sql":       {Data: []byte("--| tier: standard\n--| returning: item_by_id\nUPDATE item\nSET status = 'retired', updated_at = CURRENT_TIMESTAMP, version = version + 1\nWHERE id = {{id}} AND status <> 'retired'")},
}

type item struct {
	ID, Status           string
	Size, Version        int64
	CreatedAt, UpdatedAt time.Time
}

func scanItem(rows *sql.Rows) (item, error) {
	var it item
	err := rows.Scan(&it.ID, &it.Status, &it.Size, &it.Version, &it.CreatedAt, &it.UpdatedAt)
	it.CreatedAt, it.UpdatedAt = it.CreatedAt.UTC(), it.UpdatedAt.UTC()
	return it, err
}

// shape is the row less what differs between the two forms by construction:
// the client-supplied id and the timestamps.
func shape(it item) item { return item{Status: it.Status, Size: it.Size, Version: it.Version} }

// tracer records the statements the engine receives, by leading keyword:
// the count below the session, where a transaction's statements are
// visible too.
type tracer struct {
	mu   sync.Mutex
	sent []string
}

func (tr *tracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	first, _, _ := strings.Cut(strings.TrimSpace(data.SQL), " ")
	first, _, _ = strings.Cut(first, "\n")
	tr.mu.Lock()
	tr.sent = append(tr.sent, strings.ToUpper(first))
	tr.mu.Unlock()
	return ctx
}

func (*tracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (tr *tracer) take() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	sent := tr.sent
	tr.sent = nil
	return sent
}

// liveTraced is live over a pool whose connections report every statement
// to the returned tracer.
func liveTraced(t testing.TB) (*sqlate.DB, *tracer) {
	t.Helper()
	dsn := os.Getenv("SQLATE_DSN")
	if dsn == "" {
		t.Skip("SQLATE_DSN not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse %s: %v", dsn, err)
	}
	tr := &tracer{}
	cfg.Tracer = tr
	pool := stdlib.OpenDB(*cfg)
	if err := pool.PingContext(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return sqlate.Wrap(pool, postgres.Dialect{}), tr
}

// counted is the session each scenario runs through: *sqlate.DB with the
// calls a returning handle makes on it counted.
type counted struct {
	*sqlate.DB
	exec, query, begin int
}

func (c *counted) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	c.exec++
	return c.DB.ExecContext(ctx, q, args...)
}

func (c *counted) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	c.query++
	return c.DB.QueryContext(ctx, q, args...)
}

func (c *counted) Begin(ctx context.Context, opts ...sqlate.TxOption) (*sqlate.Tx, error) {
	c.begin++
	return c.DB.Begin(ctx, opts...)
}

// calls is what one scenario cost: the session's calls and the engine's
// statements.
type calls struct {
	exec, query, begin int
	sent               []string
}

// result is one scenario's outcome: the row, whether the command changed
// it, and the error.
type result struct {
	row     item
	changed bool
	err     error
}

// form is one compilation of the proof's files and its handles.
type form struct {
	name    string
	stmts   *query.Statements
	read    query.Rows[item]
	create  query.Returning[item]
	claim   query.RowGuard[item]
	retire  query.Returning[item]
	changed calls // a changed row: the cost the form promises
	same    calls // an unchanged row that exists
	absent  calls // no row at all
}

// Proof: the returning command's two forms return the same row. Every
// scenario runs on both: an insert; a guarded claim that succeeds, meets
// another version, is refused by its own status predicate, and finds no
// row; a predicate-form retire, then the same retire again, which changes
// nothing and returns the retired row. Each result equals an immediate read
// by the read statement, and the forms' rows are equal apart from the id and
// timestamps. The native form is one query for a changed row and two for an
// unchanged one; the fallback is a transaction it begins, holding the
// command and then its read. A column renamed out from under the read fails
// Verify, naming the single-statement form (returning).
func TestLive_ReturningTwoForms(t *testing.T) {
	ctx := context.Background()
	db, tr := liveTraced(t)
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS item")
	if _, err := db.ExecContext(ctx, `CREATE TABLE item (
		id text PRIMARY KEY,
		status text NOT NULL,
		size integer NOT NULL,
		version bigint NOT NULL DEFAULT 1,
		created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS item") })

	catalog := query.MustCatalog(postgres.Patterns(), query.Publish("live", returningFS, "patterns"))
	version := func(it item) int64 { return it.Version }
	build := func(name string, d sqlate.Dialect, changed, same, absent calls) form {
		stmts := catalog.MustCompile(returningFS, "sql", d)
		return form{
			name:    name,
			stmts:   stmts,
			read:    stmts.Statement("item_by_id").Scan(scanItem),
			create:  stmts.Statement("create_item").Returning(scanItem),
			claim:   stmts.Statement("claim_item").Returning(scanItem).Guarded("version", version),
			retire:  stmts.Statement("retire_item").Returning(scanItem),
			changed: changed, same: same, absent: absent,
		}
	}
	forms := []form{
		build("native", postgres.Dialect{},
			calls{query: 1, sent: []string{"$VERB"}},
			calls{query: 2, sent: []string{"$VERB", "SELECT"}},
			calls{query: 2, sent: []string{"$VERB", "SELECT"}}),
		build("fallback", struct{ sqlate.Dialect }{postgres.Dialect{}},
			calls{begin: 1, sent: []string{"BEGIN", "$VERB", "SELECT", "COMMIT"}},
			calls{begin: 1, sent: []string{"BEGIN", "$VERB", "SELECT", "COMMIT"}},
			calls{begin: 1, sent: []string{"BEGIN", "$VERB", "SELECT", "ROLLBACK"}}),
	}

	for _, f := range forms {
		for _, name := range []string{"create_item", "claim_item", "retire_item"} {
			st := f.stmts.Statement(name)
			if text := st.ReturningText(); (text != "") != (f.name == "native") || st.Reads() != "item_by_id" {
				t.Errorf("%s %s: Reads %q, ReturningText %q", f.name, name, st.Reads(), text)
			} else if text != "" && !strings.HasSuffix(text, "\nRETURNING id, status, size, version, created_at, updated_at") {
				t.Errorf("%s %s: ReturningText %q, want the read's bare columns", f.name, name, text)
			}
		}
		if err := f.stmts.Verify(ctx, db); err != nil {
			t.Fatalf("%s: Verify: %v", f.name, err)
		}
	}

	// run runs one scenario's operation on a counted session and reports
	// its result and what it cost.
	run := func(op func(s sqlate.Session) (item, bool, error)) (result, calls) {
		s := &counted{DB: db}
		tr.take()
		row, changed, err := op(s)
		return result{row, changed, err}, calls{exec: s.exec, query: s.query, begin: s.begin, sent: tr.take()}
	}
	guarded := func(f form, expected int64, id string) func(sqlate.Session) (item, bool, error) {
		return func(s sqlate.Session) (item, bool, error) {
			row, err := f.claim.Run(ctx, s, expected, query.Args{"id": id})
			if refused, ok := errors.AsType[*query.RefusedError[item]](err); ok {
				return refused.Row, false, err
			}
			return row, err == nil, err
		}
	}

	scenarios := []struct {
		name  string
		verb  string
		key   string // the item the outcome is read back by
		op    func(f form, id func(string) string) func(sqlate.Session) (item, bool, error)
		cost  func(f form) calls
		check func(r result) bool
	}{
		{
			"insert", "INSERT", "a",
			func(f form, id func(string) string) func(sqlate.Session) (item, bool, error) {
				return func(s sqlate.Session) (item, bool, error) {
					return f.create.One(ctx, s, query.Args{"id": id("a"), "status": "open", "size": 3})
				}
			},
			func(f form) calls { return f.changed },
			func(r result) bool { return r.err == nil && r.changed && r.row.Status == "open" && r.row.Version == 1 },
		},
		{
			"guarded success", "UPDATE", "a",
			func(f form, id func(string) string) func(sqlate.Session) (item, bool, error) {
				return guarded(f, 1, id("a"))
			},
			func(f form) calls { return f.changed },
			func(r result) bool { return r.err == nil && r.row.Status == "claimed" && r.row.Version == 2 },
		},
		{
			"version mismatch", "UPDATE", "",
			func(f form, id func(string) string) func(sqlate.Session) (item, bool, error) {
				return guarded(f, 1, id("a"))
			},
			func(f form) calls { return f.same },
			func(r result) bool {
				return errors.Is(r.err, query.ErrVersionMismatch) && strings.Contains(r.err.Error(), "expected 1, current 2")
			},
		},
		{
			"refused", "UPDATE", "b",
			func(f form, id func(string) string) func(sqlate.Session) (item, bool, error) {
				return guarded(f, 1, id("b"))
			},
			func(f form) calls { return f.same },
			func(r result) bool {
				return errors.Is(r.err, query.ErrRefused) && r.row.Status == "held" && r.row.Version == 1
			},
		},
		{
			"absent", "UPDATE", "",
			func(f form, id func(string) string) func(sqlate.Session) (item, bool, error) {
				return guarded(f, 1, id("missing"))
			},
			func(f form) calls { return f.absent },
			func(r result) bool { return errors.Is(r.err, sql.ErrNoRows) },
		},
		{
			"retire", "UPDATE", "a",
			func(f form, id func(string) string) func(sqlate.Session) (item, bool, error) {
				return func(s sqlate.Session) (item, bool, error) { return f.retire.One(ctx, s, query.Args{"id": id("a")}) }
			},
			func(f form) calls { return f.changed },
			func(r result) bool {
				return r.err == nil && r.changed && r.row.Status == "retired" && r.row.Version == 3
			},
		},
		{
			"retire again", "UPDATE", "a",
			func(f form, id func(string) string) func(sqlate.Session) (item, bool, error) {
				return func(s sqlate.Session) (item, bool, error) { return f.retire.One(ctx, s, query.Args{"id": id("a")}) }
			},
			func(f form) calls { return f.same },
			func(r result) bool {
				return r.err == nil && !r.changed && r.row.Status == "retired" && r.row.Version == 3
			},
		},
	}

	// Item b, held, is the row the claim's own predicate refuses at the
	// expected version.
	for _, f := range forms {
		if _, _, err := f.create.One(ctx, db, query.Args{"id": f.name + "-b", "status": "held", "size": 5}); err != nil {
			t.Fatalf("%s: create b: %v", f.name, err)
		}
	}

	for _, sc := range scenarios {
		var rows []item
		for _, f := range forms {
			id := func(k string) string { return f.name + "-" + k }
			r, cost := run(sc.op(f, id))
			row, changed, err := r.row, r.changed, r.err
			if !sc.check(r) {
				t.Errorf("%s, %s: row %+v, changed %v, err %v", sc.name, f.name, row, changed, err)
			}
			want := sc.cost(f)
			want.sent = slices.Clone(want.sent)
			for i, s := range want.sent {
				if s == "$VERB" {
					want.sent[i] = sc.verb
				}
			}
			if cost.exec != want.exec || cost.query != want.query || cost.begin != want.begin || !slices.Equal(cost.sent, want.sent) {
				t.Errorf("%s, %s: session exec %d query %d begin %d, engine %v; want exec %d query %d begin %d, engine %v",
					sc.name, f.name, cost.exec, cost.query, cost.begin, cost.sent, want.exec, want.query, want.begin, want.sent)
			}
			t.Logf("%s, %s: status %q version %d changed %v err %v; session exec %d query %d begin %d; engine %v",
				sc.name, f.name, row.Status, row.Version, changed, err, cost.exec, cost.query, cost.begin, cost.sent)
			if sc.key == "" {
				continue
			}
			read, err := f.read.One(ctx, db, query.Args{"id": id(sc.key)})
			if err != nil || read != row {
				t.Errorf("%s, %s: result %+v, an immediate read %+v (%v)", sc.name, f.name, row, read, err)
			}
			rows = append(rows, row)
		}
		if len(rows) == 2 && shape(rows[0]) != shape(rows[1]) {
			t.Errorf("%s: native %+v, fallback %+v; want equal apart from id and timestamps", sc.name, rows[0], rows[1])
		}
	}

	// A column renamed out from under the read: the claim and the retire do
	// not name it, so their own text still prepares and only their
	// single-statement form fails, named (returning).
	if _, err := db.ExecContext(ctx, "ALTER TABLE item RENAME COLUMN size TO bulk"); err != nil {
		t.Fatal(err)
	}
	err := forms[0].stmts.Verify(ctx, db)
	for _, want := range []string{"claim_item (returning)", "retire_item (returning)", "item_by_id"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("native Verify after the rename = %v, want it to name %s", err, want)
		}
	}
	t.Logf("native Verify after the rename: %v", err)
	if err := forms[1].stmts.Verify(ctx, db); err == nil || strings.Contains(err.Error(), "(returning)") || !strings.Contains(err.Error(), "item_by_id") {
		t.Errorf("fallback Verify after the rename = %v, want item_by_id named and no (returning) form", err)
	}
}
