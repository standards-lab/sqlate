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

	// ErrDependentObjects classifies a refusal to drop an object another
	// object still depends on (SQLSTATE class 2BP01). A multi-set
	// migration's Down or Reset raises it when it reverts sets out of
	// order and a foreign key from a set above still references the set
	// being reverted. A dialect's MapError wraps it as
	// fmt.Errorf("%w: %w", ErrDependentObjects, err) so errors.Is matches
	// the class and the driver's error stays reachable.
	ErrDependentObjects = errors.New("dependent objects still exist")

	// ErrSerializationFailure classifies a serialization failure under
	// SERIALIZABLE isolation (SQLSTATE 40001). Two migrator starters
	// raise it when they race under the migration lock and the engine
	// offers no native advisory lock to serialize them. A dialect maps
	// its engine's form to it, wrapped in the same dual form as
	// [ErrDependentObjects].
	ErrSerializationFailure = errors.New("serialization failure")
)

// ConstraintError is a classified constraint violation: the class sentinel,
// the driver's own error, and the violated constraint's name, table, and
// column when the driver exposes them. A driver exposes them unevenly: a
// not-null violation names a column and no constraint, so Column is the only
// handle that case offers. Unwrap yields both wrapped errors, so errors.Is
// matches the class and errors.As finds the driver error, and a consumer maps
// the constraint name, or the column name when there is no constraint name,
// to its field.
type ConstraintError struct {
	Constraint string
	Table      string
	Column     string
	Class      error
	Err        error
}

func (e *ConstraintError) Error() string {
	switch {
	case e.Constraint != "":
		return fmt.Sprintf("%v on constraint %q: %v", e.Class, e.Constraint, e.Err)
	case e.Column != "":
		return fmt.Sprintf("%v on column %q: %v", e.Class, e.Column, e.Err)
	default:
		return fmt.Sprintf("%v: %v", e.Class, e.Err)
	}
}

func (e *ConstraintError) Unwrap() []error {
	return []error{e.Class, e.Err}
}
