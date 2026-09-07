package sqlate

import (
	"errors"
	"fmt"
)

var (
	// ErrConnectionFailed classifies a failure to reach the engine: a
	// failure to obtain a connection or begin a transaction, and a network
	// error or driver.ErrBadConn raised by any session call. It is wrapped
	// as fmt.Errorf("%w: %w", ErrConnectionFailed, err) so errors.Is
	// matches it and the driver's error stays reachable.
	ErrConnectionFailed = errors.New("database connection failed")

	// ErrInvalidValue classifies a data exception: a bound value the engine
	// could not read as the type it was cast to (SQLSTATE class 22). It is
	// the engine-side half of request validation (a filter value that is
	// not a uuid, a date that is not a date), and a dialect maps its
	// engine's form to it, wrapped in the same dual form.
	ErrInvalidValue = errors.New("invalid value")

	// ErrUniqueViolation classifies a unique-constraint violation. A
	// dialect's MapError wraps it in a [ConstraintError]; errors.Is
	// matches the class.
	ErrUniqueViolation = errors.New("unique constraint violation")

	// ErrForeignKeyViolation classifies a foreign-key-constraint violation,
	// wrapped the same way as [ErrUniqueViolation].
	ErrForeignKeyViolation = errors.New("foreign key constraint violation")

	// ErrCheckViolation classifies a check-constraint violation, wrapped
	// the same way as [ErrUniqueViolation].
	ErrCheckViolation = errors.New("check constraint violation")

	// ErrNotNullViolation classifies a not-null-constraint violation,
	// wrapped the same way as [ErrUniqueViolation].
	ErrNotNullViolation = errors.New("not-null constraint violation")
)

// ConstraintError is a classified constraint violation: the class sentinel,
// the driver's own error, and the violated constraint's name when the driver
// exposes it. Unwrap yields both wrapped errors, so errors.Is matches the
// class and errors.As finds the driver error, and a consumer maps the
// constraint name to its field.
type ConstraintError struct {
	Constraint string
	Class      error
	Err        error
}

func (e *ConstraintError) Error() string {
	if e.Constraint == "" {
		return fmt.Sprintf("%v: %v", e.Class, e.Err)
	}
	return fmt.Sprintf("%v on constraint %q: %v", e.Class, e.Constraint, e.Err)
}

func (e *ConstraintError) Unwrap() []error {
	return []error{e.Class, e.Err}
}
