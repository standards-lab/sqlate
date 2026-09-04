package postgres

import "errors"

// ErrLockNotHeld reports an Unlock of a name this session did not hold; the
// engine answers false rather than failing, so the dialect makes it an
// error.
var ErrLockNotHeld = errors.New("advisory lock not held by this session")
