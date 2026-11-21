//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

func scanItem(rows query.Row) (item, error) {
	var it item
	err := rows.Scan(&it.ID, &it.Status, &it.Size, &it.Version, &it.CreatedAt, &it.UpdatedAt)
	it.CreatedAt, it.UpdatedAt = it.CreatedAt.UTC(), it.UpdatedAt.UTC()
	return it, err
}

func shape(it item) item { return item{Status: it.Status, Size: it.Size, Version: it.Version} }

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

func (c *counted) Begin(ctx context.Context, opts *sql.TxOptions) (*sqlate.Tx, error) {
	c.begin++
	return c.DB.Begin(ctx, opts)
}

type calls struct {
	exec, query, begin int
	sent               []string
}

type result struct {
	row     item
	changed bool
	err     error
}

type form struct {
	name    string
	stmts   *query.Statements
	read    query.Rows[item]
	create  query.Returning[item]
	claim   query.RowGuard[item]
	retire  query.Returning[item]
	changed calls
	same    calls
	absent  calls
}

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
		key   string
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

var txFS = fstest.MapFS{
	"sql/tx_item_by_id.sql": {Data: []byte("--| tier: standard\nSELECT i.id, i.status, i.size, i.version, i.created_at, i.updated_at FROM tx_item i WHERE i.id = {{id}}")},
	"sql/tx_create.sql":     {Data: []byte("--| tier: standard\n--| returning: tx_item_by_id\nINSERT INTO tx_item (id, status, size) VALUES ({{id}}, {{status}}, {{size}})")},
	"sql/tx_claim.sql":      {Data: []byte("--| tier: standard\n--| returning: tx_item_by_id\nUPDATE tx_item\nSET status = 'claimed', {{> sql.guard_set}}\nWHERE {{> sql.guard_where}} AND status = 'open'")},
	"sql/tx_retire.sql":     {Data: []byte("--| tier: standard\n--| returning: tx_item_by_id\nUPDATE tx_item\nSET status = 'retired', version = version + 1\nWHERE id = {{id}} AND status <> 'retired'")},
}

func TestLive_ReturningInsideACallersTransaction(t *testing.T) {
	ctx := context.Background()
	db, tr := liveTraced(t)
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS tx_item")
	if _, err := db.ExecContext(ctx, `CREATE TABLE tx_item (
		id text PRIMARY KEY,
		status text NOT NULL,
		size integer NOT NULL,
		version bigint NOT NULL DEFAULT 1,
		created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS tx_item") })

	catalog := query.MustCatalog(postgres.Patterns())
	version := func(it item) int64 { return it.Version }
	for _, f := range []struct {
		name string
		d    sqlate.Dialect
		sent []string
	}{
		{"native", postgres.Dialect{}, []string{
			"BEGIN",
			"INSERT",
			"UPDATE",
			"UPDATE", "SELECT",
			"UPDATE",
			"UPDATE", "SELECT",
			"COMMIT",
		}},
		{"fallback", struct{ sqlate.Dialect }{postgres.Dialect{}}, []string{
			"BEGIN",
			"INSERT", "SELECT",
			"UPDATE", "SELECT",
			"UPDATE", "SELECT",
			"UPDATE", "SELECT",
			"UPDATE", "SELECT",
			"COMMIT",
		}},
	} {
		stmts := catalog.MustCompile(txFS, "sql", f.d)
		if (stmts.Statement("tx_retire").ReturningText() != "") != (f.name == "native") {
			t.Fatalf("%s: ReturningText %q", f.name, stmts.Statement("tx_retire").ReturningText())
		}
		create := stmts.Statement("tx_create").Returning(scanItem)
		claim := stmts.Statement("tx_claim").Returning(scanItem).Guarded("version", version)
		retire := stmts.Statement("tx_retire").Returning(scanItem)
		id := f.name + "-a"
		args := query.Args{"id": id}

		tr.take()
		final, err := db.Transact(ctx, func(tx *sqlate.Tx) (item, error) {
			row, changed, err := create.One(ctx, tx, query.Args{"id": id, "status": "open", "size": 3})
			if err != nil || !changed || row.Version != 1 {
				return row, fmt.Errorf("create: %+v, changed %v: %w", row, changed, err)
			}
			if row, err = claim.Run(ctx, tx, 1, args); err != nil || row.Status != "claimed" || row.Version != 2 {
				return row, fmt.Errorf("claim: %+v: %w", row, err)
			}
			if _, err = claim.Run(ctx, tx, 1, args); !errors.Is(err, query.ErrVersionMismatch) {
				return row, fmt.Errorf("claim again: %w, want ErrVersionMismatch", err)
			}
			if row, changed, err = retire.One(ctx, tx, args); err != nil || !changed || row.Version != 3 {
				return row, fmt.Errorf("retire: %+v, changed %v: %w", row, changed, err)
			}
			if row, changed, err = retire.One(ctx, tx, args); err != nil || changed || row.Status != "retired" || row.Version != 3 {
				return row, fmt.Errorf("retire again: %+v, changed %v: %w", row, changed, err)
			}
			return row, nil
		}, nil)
		sent := tr.take()
		if err != nil {
			t.Errorf("%s: Transact: %v", f.name, err)
			continue
		}
		if !slices.Equal(sent, f.sent) {
			t.Errorf("%s: engine %v, want %v", f.name, sent, f.sent)
		}
		t.Logf("%s: final %q version %d; engine %v", f.name, final.Status, final.Version, sent)
		read, err := stmts.Statement("tx_item_by_id").Scan(scanItem).One(ctx, db, args)
		if err != nil || read != final {
			t.Errorf("%s: after commit %+v (%v), want the unit's last row %+v", f.name, read, err, final)
		}
	}
}
