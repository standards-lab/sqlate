package migrate

import (
	"errors"
	"fmt"
)

var (
	// ErrNoLocker reports a run that needs the lock on a dialect without the
	// capability; Options.Unlocked opts out.
	ErrNoLocker = errors.New("migrate: dialect has no lock capability")
	// ErrDirty is the class a *DirtyError unwraps to.
	ErrDirty = errors.New("migrate: schema is dirty")
	// ErrPending is the class a *PendingError unwraps to.
	ErrPending = errors.New("migrate: migrations pending")
	// ErrUnknownVersion is the class an *UnknownVersionError unwraps to.
	ErrUnknownVersion = errors.New("migrate: history does not match the migration set")
	// ErrNoDown reports a Down of a migration that has no down text.
	ErrNoDown = errors.New("migrate: migration has no down")
	// ErrVersionNotFound reports a Force to a version outside the set.
	ErrVersionNotFound = errors.New("migrate: version not in the migration set")
)

// DirtyError reports a version whose non-transactional migration failed
// midway: the history row is marked dirty and every run refuses until Force
// clears it. Err is the failure that dirtied it, when this run caused it.
type DirtyError struct {
	Version int
	Err     error
}

func (e *DirtyError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%v: version %d failed: %v", ErrDirty, e.Version, e.Err)
	}
	return fmt.Sprintf("%v: version %d", ErrDirty, e.Version)
}

func (e *DirtyError) Unwrap() []error {
	if e.Err != nil {
		return []error{ErrDirty, e.Err}
	}
	return []error{ErrDirty}
}

// PendingError reports migrations in the set the history has not applied.
type PendingError struct{ Versions []int }

func (e *PendingError) Error() string {
	return fmt.Sprintf("%v: %v", ErrPending, e.Versions)
}

func (e *PendingError) Unwrap() error { return ErrPending }

// UnknownVersionError reports an applied row the migration set does not
// carry at that position: a version the set lacks, or a name that differs.
type UnknownVersionError struct {
	Version int
	Name    string
}

func (e *UnknownVersionError) Error() string {
	return fmt.Sprintf("%v: applied %d %q", ErrUnknownVersion, e.Version, e.Name)
}

func (e *UnknownVersionError) Unwrap() error { return ErrUnknownVersion }
