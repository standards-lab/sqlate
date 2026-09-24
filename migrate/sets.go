package migrate

import (
	"context"
	"database/sql"
	"fmt"
)

// DefaultTable is the history table a Set uses when its Table is empty. At
// most one of a Migrator's sets may leave Table empty.
const DefaultTable = "schema_version"

// Set is one migration layer as a library ships it or a program declares
// it: a name, the history table its migrations are recorded in (empty for
// DefaultTable), and its migrations in version order. A Migrator runs
// several, declared bottom-first: a set's migrations may reference the
// objects of the sets declared before it, never after.
type Set struct {
	Name       string
	Table      string
	Migrations []Migration
}

// SetStatus is one layer's state as Status reads it, without the lock: the
// layer's name and history table, the highest applied version (zero when
// nothing is applied), the layer's latest version, the migrations the
// history has not applied, and whether the history holds a dirty row.
type SetStatus struct {
	Name    string
	Table   string
	Version int
	Latest  int
	Pending []Migration
	Dirty   bool
}

// layer is one set as the migrator holds it: the table after defaulting,
// the validated copy of the migrations, and the statements rendered for
// the table.
type layer struct {
	name       string
	table      string
	migrations []Migration
	sql        statements
}

// Layer is a handle on one of a Migrator's sets. Its verbs act on that set
// alone, under the migrator's lock and its ordering rules: a revert is
// refused while a layer above has applied migrations, and an apply is
// refused while a layer below has pending ones. The zero Layer is not
// usable; Migrator.Layers and Migrator.Layer return usable ones.
type Layer struct {
	m *Migrator
	i int
}

// Name returns the set's name.
func (l Layer) Name() string { return l.m.layers[l.i].name }

// Table returns the set's history table, after DefaultTable is applied.
func (l Layer) Table() string { return l.m.layers[l.i].table }

// Migrations returns a copy of the set's migrations.
func (l Layer) Migrations() []Migration { return copyOf(l.m.layers[l.i].migrations) }

// Version reads the set's history head without taking the lock. A missing
// history table is the zero Version.
func (l Layer) Version(ctx context.Context) (Version, error) {
	return l.m.version(ctx, l.m.layers[l.i])
}

// Verify checks, without the lock, that the set's history is a clean,
// complete prefix of its migrations. Every fault is a *SetError naming the
// set: a dirty row unwraps to a *DirtyError, a row the set does not carry
// to an *UnknownVersionError, and unapplied migrations to a *PendingError.
func (l Layer) Verify(ctx context.Context) error { return l.m.verify(ctx, l.m.layers[l.i]) }

// Status reads the set's state without the lock.
func (l Layer) Status(ctx context.Context) (SetStatus, error) {
	return l.m.status(ctx, l.m.layers[l.i])
}

// Steps applies the set's next n pending migrations when n is positive, or
// reverts its last -n applied when negative; fewer remaining is not an
// error, so -len(Migrations()) reverts the whole set. Every set's history
// is checked first. A positive n is refused with ErrBelowPending while a
// layer below has pending migrations; a negative n is refused with
// ErrAboveApplied while a layer above has applied ones.
func (l Layer) Steps(ctx context.Context, n int) error {
	if n == 0 {
		return nil
	}
	return l.m.locked(ctx, func(ctx context.Context, conn *sql.Conn) error {
		applied, err := l.m.preflight(ctx, conn)
		if err != nil {
			return err
		}
		if n > 0 {
			if err := l.m.belowPending(applied, l.i); err != nil {
				return err
			}
			return l.m.applyPending(ctx, conn, l.m.layers[l.i], applied[l.i], n)
		}
		if err := l.m.aboveApplied(applied, l.i); err != nil {
			return err
		}
		return l.m.revertApplied(ctx, conn, l.m.layers[l.i], applied[l.i], -n)
	})
}

// Down reverts the set's n most recently applied migrations; n larger than
// the applied count reverts them all. A caller that wants every set
// reverted without dropping any history table iterates Layers in reverse
// and calls Down(ctx, len(l.Migrations())) on each; Migrator.Reset reverts
// every set and drops the tables as well.
func (l Layer) Down(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	return l.Steps(ctx, -n)
}

// Force sets the set's history to version as an operator override: rows
// above it are deleted, and every migration up to and including it has its
// row marked clean, inserted where absent, so the history is the set's
// prefix through version however far below it the history stood. Version 0
// empties the history. Nothing runs against the schema itself, and no
// set's history is checked first, since Force is the repair for a dirty
// one.
func (l Layer) Force(ctx context.Context, version int) error {
	lay := l.m.layers[l.i]
	var through []Migration
	if version != 0 {
		found := false
		for i, mig := range lay.migrations {
			if mig.Version == version {
				through, found = lay.migrations[:i+1], true
				break
			}
		}
		if !found {
			return &SetError{Set: lay.name, Err: fmt.Errorf("%w: %d", ErrVersionNotFound, version)}
		}
	}
	return l.m.locked(ctx, func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, lay.sql.create); err != nil {
			return &SetError{Set: lay.name, Err: l.m.db.MapError(err)}
		}
		if _, err := conn.ExecContext(ctx, lay.sql.delAbove, version); err != nil {
			return &SetError{Set: lay.name, Err: l.m.db.MapError(err)}
		}
		for _, mig := range through {
			res, err := conn.ExecContext(ctx, lay.sql.setDirty, false, mig.Version)
			if err != nil {
				return &SetError{Set: lay.name, Err: l.m.db.MapError(err)}
			}
			if n, _ := res.RowsAffected(); n == 0 {
				if _, err := conn.ExecContext(ctx, lay.sql.insert, mig.Version, mig.Name, false); err != nil {
					return &SetError{Set: lay.name, Err: l.m.db.MapError(err)}
				}
			}
		}
		if version != 0 {
			l.m.log("migration forced", "set", lay.name, "version", version)
		}
		return nil
	})
}

// copyOf returns a copy of ms, so a caller never holds a migrator's slice.
func copyOf(ms []Migration) []Migration {
	out := make([]Migration, len(ms))
	copy(out, ms)
	return out
}
