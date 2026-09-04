package query

import (
	"context"
	"database/sql"
	"iter"

	"github.com/standards-lab/sqlate"
)

// ScanFunc reads the current row into a T. The consumer writes one per row
// shape; the SELECT list is the scan order.
type ScanFunc[T any] func(*sql.Rows) (T, error)

// Scalar is the ScanFunc for a single-column row.
func Scalar[T any](rows *sql.Rows) (T, error) {
	var v T
	err := rows.Scan(&v)
	return v, err
}

// Rows is a statement bound to a scan function: the typed handle a domain
// holds for a query that returns rows.
type Rows[T any] struct {
	stmt Statement
	scan ScanFunc[T]
}

// Statement returns the underlying statement.
func (r Rows[T]) Statement() Statement { return r.stmt }

// One returns the first row; no row is sql.ErrNoRows, unmapped.
func (r Rows[T]) One(ctx context.Context, s sqlate.Session, args Args) (T, error) {
	var zero T
	rows, err := r.stmt.query(ctx, s, args)
	if err != nil {
		return zero, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return zero, mapErr(s, err)
		}
		return zero, sql.ErrNoRows
	}
	v, err := r.scan(rows)
	if err != nil {
		return zero, mapErr(s, err)
	}
	return v, mapErr(s, rows.Err())
}

// All returns every row.
func (r Rows[T]) All(ctx context.Context, s sqlate.Session, args Args) ([]T, error) {
	var out []T
	for v, err := range r.Each(ctx, s, args) {
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Each yields the rows one at a time; the row set closes when the loop
// ends, by exhaustion or by break. A failure is the final pair's error.
func (r Rows[T]) Each(ctx context.Context, s sqlate.Session, args Args) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		rows, err := r.stmt.query(ctx, s, args)
		if err != nil {
			yield(zero, err)
			return
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			v, err := r.scan(rows)
			if err != nil {
				yield(zero, mapErr(s, err))
				return
			}
			if !yield(v, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(zero, mapErr(s, err))
		}
	}
}
