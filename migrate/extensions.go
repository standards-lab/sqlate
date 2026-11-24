package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Extension declares one database extension the migrator manages, by name
// and without a version: the version is the one the database image offers
// as the extension's default. Removed marks a declaration whose extension
// the migrator drops instead of activating. Deleting a declaration only
// stops the migrator from managing the extension; it never drops it.
type Extension struct {
	Name    string
	Removed bool
}

// ExtensionCatalog is the dialect capability that manages extensions: the
// engine-specific statements the migrator runs for each Extension
// declaration, returned as text, as Catalog's are. A dialect provides it by
// implementing the three methods; New refuses a declaration on a dialect
// that does not. Each method that takes a name quotes it as the engine
// requires.
type ExtensionCatalog interface {
	// ExtensionState returns the query that yields one row with three
	// columns for the extension whose name is the query's one argument,
	// bound at param, the dialect's placeholder: the installed version
	// (NULL when the extension is not installed), the default version the
	// database offers (NULL when it does not offer the extension), and
	// whether an update path leads from the installed version to the
	// default (a boolean). No row reads as neither installed nor offered.
	ExtensionState(param string) string
	// ActivateExtension returns the statements, run in order, that create
	// the extension when it does not exist and then update it to its
	// default version.
	ActivateExtension(name string) []string
	// RemoveExtension returns the statement that drops the extension when
	// it exists, refusing (RESTRICT) when an object depends on it.
	RemoveExtension(name string) string
}

// ExtensionStatus is one declaration's state as Migrator.Extensions reads
// it, without the lock: the declaration's name and removed flag, the
// installed version (empty when not installed), the default version the
// database offers (empty when it does not offer the extension), and whether
// the declaration is pending. An active declaration is pending while its
// extension is missing but offered, or installed behind the default with an
// update path to it; an extension the database does not offer, or one with
// no update path, is not pending. A removed declaration is pending while
// its extension is still installed.
type ExtensionStatus struct {
	Name      string
	Removed   bool
	Installed string
	Default   string
	Pending   bool
}

type extensionState struct {
	installed, def string
	path           bool
}

func validateExtensions(declared []Extension, catalog ExtensionCatalog) error {
	if len(declared) > 0 && catalog == nil {
		return errors.New("migrate: extensions declared on a dialect without the extension capability")
	}
	seen := map[string]bool{}
	for _, e := range declared {
		if e.Name == "" {
			return errors.New("migrate: an extension has no name")
		}
		if seen[e.Name] {
			return fmt.Errorf("migrate: extension %q is declared twice", e.Name)
		}
		seen[e.Name] = true
	}
	return nil
}

// Extensions reads every extension declaration's state in declared order.
// It runs on the pool without the lock, so it never waits on a running Up.
// An extension the database does not offer reports an empty Default and is
// not an error.
func (m *Migrator) Extensions(ctx context.Context) ([]ExtensionStatus, error) {
	out := make([]ExtensionStatus, 0, len(m.opts.Extensions))
	for _, e := range m.opts.Extensions {
		st, err := m.extensionState(ctx, m.db, e)
		if err != nil {
			return nil, err
		}
		out = append(out, ExtensionStatus{
			Name:      e.Name,
			Removed:   e.Removed,
			Installed: st.installed,
			Default:   st.def,
			Pending:   errors.Is(classify(e, st), ErrPending),
		})
	}
	return out, nil
}

func (m *Migrator) extensionState(ctx context.Context, q querier, e Extension) (extensionState, error) {
	var installed, def sql.NullString
	var path sql.NullBool
	if _, err := m.queryOne(ctx, q, m.extensionQuery, []any{e.Name}, &installed, &def, &path); err != nil {
		return extensionState{}, &ExtensionError{Extension: e.Name, Err: err}
	}
	return extensionState{installed: installed.String, def: def.String, path: path.Bool}, nil
}

func classify(e Extension, st extensionState) error {
	switch {
	case e.Removed && st.installed != "":
		return &ExtensionPendingError{Removed: true, Installed: st.installed, Default: st.def}
	case e.Removed:
		return nil
	case st.def == "":
		return ErrExtensionUnavailable
	case st.installed == st.def:
		return nil
	case st.installed != "" && !st.path:
		return &NoUpdatePathError{Installed: st.installed, Default: st.def}
	}
	return &ExtensionPendingError{Installed: st.installed, Default: st.def}
}

func (m *Migrator) verifyExtensions(ctx context.Context) error {
	for _, e := range m.opts.Extensions {
		st, err := m.extensionState(ctx, m.db, e)
		if err != nil {
			return err
		}
		if err := classify(e, st); err != nil {
			return &ExtensionError{Extension: e.Name, Err: err}
		}
	}
	return nil
}

func (m *Migrator) activateExtensions(ctx context.Context, conn *sql.Conn) error {
	type activation struct {
		e  Extension
		st extensionState
	}
	var due []activation
	for _, e := range m.opts.Extensions {
		if e.Removed {
			continue
		}
		st, err := m.extensionState(ctx, conn, e)
		if err != nil {
			return err
		}
		switch err := classify(e, st); {
		case err == nil:
			continue
		case !errors.Is(err, ErrPending):
			return &ExtensionError{Extension: e.Name, Err: err}
		}
		due = append(due, activation{e: e, st: st})
	}
	for _, a := range due {
		err := m.inTx(ctx, conn, func(tx *sql.Tx) error {
			for _, stmt := range m.extensions.ActivateExtension(a.e.Name) {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return &ExtensionError{Extension: a.e.Name, Err: err}
		}
		if a.st.installed == "" {
			m.log("extension activated", "extension", a.e.Name, "version", a.st.def)
		} else {
			m.log("extension activated", "extension", a.e.Name, "version", a.st.def, "from", a.st.installed)
		}
	}
	return nil
}

func (m *Migrator) removeExtensions(ctx context.Context, conn *sql.Conn) error {
	for _, e := range m.opts.Extensions {
		if !e.Removed {
			continue
		}
		st, err := m.extensionState(ctx, conn, e)
		if err != nil {
			return err
		}
		if classify(e, st) == nil {
			continue
		}
		err = m.inTx(ctx, conn, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, m.extensions.RemoveExtension(e.Name))
			return err
		})
		if err != nil {
			return &ExtensionError{Extension: e.Name, Err: err}
		}
		m.log("extension removed", "extension", e.Name, "version", st.installed)
	}
	return nil
}
