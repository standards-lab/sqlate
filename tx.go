package sqlate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TxOption sets a field of the sql.TxOptions Begin opens with.
type TxOption func(*sql.TxOptions)

// Isolation requests an isolation level; the guard's serializable claim has
// a code path through it.
func Isolation(level sql.IsolationLevel) TxOption {
	return func(o *sql.TxOptions) { o.Isolation = level }
}

// ReadOnly opens the transaction read-only.
func ReadOnly() TxOption {
	return func(o *sql.TxOptions) { o.ReadOnly = true }
}

// Tx is one transaction, a Session whose errors are mapped on every call
// and on commit.
type Tx struct {
	tx      *sql.Tx
	dialect Dialect
}

// Begin opens a transaction with the options applied; a failure to begin
// wraps ErrConnectionFailed.
func (d *DB) Begin(ctx context.Context, opts ...TxOption) (*Tx, error) {
	var o sql.TxOptions
	for _, opt := range opts {
		opt(&o)
	}
	tx, err := d.pool.BeginTx(ctx, &o)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	return &Tx{tx: tx, dialect: d.dialect}, nil
}

// MapError routes err through the dialect; nil stays nil.
func (t *Tx) MapError(err error) error {
	if err == nil {
		return nil
	}
	return t.dialect.MapError(err)
}

func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := t.tx.ExecContext(ctx, query, args...)
	return res, t.MapError(err)
}

func (t *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := t.tx.QueryContext(ctx, query, args...)
	return rows, t.MapError(err)
}

func (t *Tx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	st, err := t.tx.PrepareContext(ctx, query)
	return st, t.MapError(err)
}

// Commit commits, mapping the error: the one place a violation deferred to
// COMMIT can be classified.
func (t *Tx) Commit() error { return t.MapError(t.tx.Commit()) }

// Rollback rolls back. sql.ErrTxDone passes through unmapped.
func (t *Tx) Rollback() error { return t.tx.Rollback() }

// Transact runs fn as one unit of work with a result: begin with opts, fn,
// commit on success. On fn's error it rolls back and returns that error,
// with a rollback failure joined onto it. A panic in fn rolls back and
// re-panics, so no transaction leaks to the pool's reaper.
func (d *DB) Transact[T any](ctx context.Context, fn func(*Tx) (T, error), opts ...TxOption) (T, error) {
	var zero T
	tx, err := d.Begin(ctx, opts...)
	if err != nil {
		return zero, err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	result, err := fn(tx)
	if err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			err = errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
