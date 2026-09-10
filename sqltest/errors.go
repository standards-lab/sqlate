package sqltest

import "errors"

// The driver's own failures, each the counterpart of a failure a real driver
// or engine raises. A statement no response was scripted for is a statement
// the test did not expect to run; a response whose shape does not fit the
// call is a script defect. An argument count that does not match the
// statement's placeholders is what the engine rejects at bind time.
var (
	ErrUnscripted = errors.New("sqltest: unscripted call")
	ErrScript     = errors.New("sqltest: response does not fit the call")
	ErrArguments  = errors.New("sqltest: argument count does not match the placeholders")
)

// MappedError marks an error that passed through Dialect.MapError.
type MappedError struct{ Err error }

func (e *MappedError) Error() string { return "mapped: " + e.Err.Error() }

func (e *MappedError) Unwrap() error { return e.Err }
