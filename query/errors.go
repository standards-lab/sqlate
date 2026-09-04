package query

import (
	"errors"
	"fmt"
)

// ErrTransactionRequired reports a statement headed "-- transaction:
// required" run on a session that is not a transaction: the one silent
// hazard, a transaction-scoped lock outside one, made loud.
var ErrTransactionRequired = errors.New("query: statement requires a transaction")

// ArgumentError reports a parameter the Args did not carry. It is a
// programming error in the caller, not request input, and matches no
// request sentinel.
type ArgumentError struct {
	Statement string
	Name      string
	// Problem says what was wrong with an argument that was present; empty
	// means it was missing.
	Problem string
}

func (e *ArgumentError) Error() string {
	if e.Problem != "" {
		return fmt.Sprintf("query: %s: argument %q: %s", e.Statement, e.Name, e.Problem)
	}
	return fmt.Sprintf("query: %s: missing argument %q", e.Statement, e.Name)
}

// ErrDirectives is the request sentinel: every error a read request's
// declarations can cause unwraps to it, so a consumer's matcher is one
// errors.Is for 400.
var ErrDirectives = errors.New("query: invalid declarations")

// FieldUse names the declaration position where a contract field was
// referenced, carried by UnknownFieldError.
type FieldUse string

const (
	FieldUseSort   FieldUse = "sort"
	FieldUseFilter FieldUse = "filter"
)

// UnknownFieldError reports a sort or filter naming a field the projection
// does not declare. It is the field contract's boundary: the name never
// reaches the SQL.
type UnknownFieldError struct {
	Field string
	Use   FieldUse
}

func (e *UnknownFieldError) Error() string {
	return fmt.Sprintf("query: unknown %s field %q", e.Use, e.Field)
}

func (e *UnknownFieldError) Unwrap() error { return ErrDirectives }

// UnknownOperatorError reports a filter carrying an operator the vocabulary
// does not define.
type UnknownOperatorError struct {
	Op Op
}

func (e *UnknownOperatorError) Error() string {
	return fmt.Sprintf("query: unknown filter operator %q", string(e.Op))
}

func (e *UnknownOperatorError) Unwrap() error { return ErrDirectives }

// InvalidValueError reports a filter value the projection or the engine
// could not use: a value of the wrong shape for its operator, named by
// field, or a bound value the engine rejected for the field's type, in
// which case Field is empty and Err carries the engine's reason.
type InvalidValueError struct {
	Field string
	Err   error
}

func (e *InvalidValueError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("query: invalid value for %q: %v", e.Field, e.Err)
	}
	return fmt.Sprintf("query: %v", e.Err)
}

func (e *InvalidValueError) Unwrap() []error { return []error{ErrDirectives, e.Err} }
