package sqltest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
)

// Open returns a pool over a fresh Recorder, closed when the test ends.
func Open(t testing.TB, responses ...Response) (*sql.DB, *Recorder) {
	t.Helper()
	rec := &Recorder{}
	rec.Queue(responses...)
	pool := sql.OpenDB(&connector{rec: rec})
	t.Cleanup(func() { _ = pool.Close() })
	return pool, rec
}

type connector struct{ rec *Recorder }

func (c *connector) Connect(context.Context) (driver.Conn, error) {
	return &conn{rec: c.rec}, nil
}

func (c *connector) Driver() driver.Driver { return drv{} }

type drv struct{}

func (drv) Open(string) (driver.Conn, error) {
	return nil, errors.New("sqltest: open by DSN unsupported; use Open")
}

// conn implements the context-aware driver interfaces: exec, query,
// prepare, begin with options, ping, and the named-value checker that
// accepts every argument unconverted.
type conn struct{ rec *Recorder }

var (
	_ driver.ExecerContext      = (*conn)(nil)
	_ driver.QueryerContext     = (*conn)(nil)
	_ driver.ConnPrepareContext = (*conn)(nil)
	_ driver.ConnBeginTx        = (*conn)(nil)
	_ driver.Pinger             = (*conn)(nil)
	_ driver.NamedValueChecker  = (*conn)(nil)
)

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *conn) PrepareContext(_ context.Context, query string) (driver.Stmt, error) {
	c.rec.record(Call{Op: OpPrepare, SQL: query})
	if c.rec.FailPrepare != nil {
		if err := c.rec.FailPrepare(query); err != nil {
			return nil, err
		}
	}
	return &stmt{rec: c.rec, query: query}, nil
}

func (c *conn) Close() error { return nil }

func (c *conn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *conn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.rec.record(Call{Op: OpBegin, TxOptions: opts})
	if c.rec.FailBegin != nil {
		return nil, c.rec.FailBegin
	}
	return &tx{rec: c.rec}, nil
}

func (c *conn) Ping(context.Context) error { return c.rec.FailPing }

func (c *conn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *conn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.rec.exec(query, args)
}

func (c *conn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.rec.query(query, args)
}

func (r *Recorder) exec(query string, args []driver.NamedValue) (driver.Result, error) {
	resp, err := r.next(OpExec, query, args)
	if err != nil {
		return nil, err
	}
	if resp.Err != nil {
		return nil, resp.Err
	}
	return driver.RowsAffected(resp.Affected), nil
}

func (r *Recorder) query(query string, args []driver.NamedValue) (driver.Rows, error) {
	resp, err := r.next(OpQuery, query, args)
	if err != nil {
		return nil, err
	}
	if resp.Err != nil {
		return nil, resp.Err
	}
	r.mu.Lock()
	r.rowsOpen++
	r.mu.Unlock()
	return &rows{rec: r, cols: resp.Columns, rows: resp.Rows}, nil
}

type stmt struct {
	rec   *Recorder
	query string
}

var (
	_ driver.StmtExecContext   = (*stmt)(nil)
	_ driver.StmtQueryContext  = (*stmt)(nil)
	_ driver.NamedValueChecker = (*stmt)(nil)
)

func (s *stmt) Close() error { return nil }

// NumInput returns -1 so database/sql skips its argument-count check; the
// scripted driver does not parse SQL.
func (s *stmt) NumInput() int { return -1 }

func (s *stmt) CheckNamedValue(*driver.NamedValue) error { return nil }

func (s *stmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, errors.New("sqltest: use ExecContext")
}

func (s *stmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("sqltest: use QueryContext")
}

func (s *stmt) ExecContext(_ context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.rec.exec(s.query, args)
}

func (s *stmt) QueryContext(_ context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.rec.query(s.query, args)
}

type tx struct{ rec *Recorder }

func (t *tx) Commit() error {
	t.rec.record(Call{Op: OpCommit})
	return t.rec.FailCommit
}

func (t *tx) Rollback() error {
	t.rec.record(Call{Op: OpRollback})
	return t.rec.FailRollback
}

type rows struct {
	rec  *Recorder
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *rows) Columns() []string { return r.cols }

func (r *rows) Close() error {
	r.rec.mu.Lock()
	r.rec.rowsClose++
	r.rec.mu.Unlock()
	return nil
}

func (r *rows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}
