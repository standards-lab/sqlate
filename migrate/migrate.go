package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"

	"github.com/standards-lab/sqlate"
)

// Options configures a Migrator. Every field has a default.
type Options struct {
	// LockName names the lock a run holds; default "migrate.<table>" over
	// the top set's history table, so a program's older single-set binary
	// and its multi-set successor contend for the same lock mid-rollout,
	// and no lock of the program's own can collide with it by number.
	LockName string
	// Unlocked allows runs on a dialect without the lock capability, or with
	// it, without taking the lock. Its intended shape is a caller that holds
	// a lock of its own around every run; concurrent starters without one
	// are unsafe.
	Unlocked bool
	// Logger records each applied and reverted migration, each forced
	// version, and each history table Reset drops; nil is silent.
	Logger *slog.Logger
}

// Version is the history's head: the highest applied version and whether
// its row is dirty. A zero Version means nothing is applied.
type Version struct {
	Version int
	Dirty   bool
}

// Migrator runs one or more migration sets against a database: the sets in
// declared order, each over its own history table, every run on one pinned
// connection under one lock. Its methods without a set act on the top set,
// except Up, Verify, Reset, and Status, which cover every set.
type Migrator struct {
	db     *sqlate.DB
	layers []*layer
	opts   Options
	locker sqlate.Locker
}

var tableName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// New validates the sets and prepares the migrator: at least one set, each
// with a name no other set uses and a history table (DefaultTable when
// empty, so at most one set may leave it empty) no other set uses, and each
// set's migrations with positive, strictly increasing versions and names
// and up texts present. The lock capability and the Catalog are taken from
// the dialect when it has them. New performs no I/O.
func New(db *sqlate.DB, sets []Set, opts Options) (*Migrator, error) {
	if db == nil {
		return nil, errors.New("migrate: nil db")
	}
	if len(sets) == 0 {
		return nil, errors.New("migrate: no sets")
	}
	locker, _ := db.Dialect().(sqlate.Locker)
	catalog, ok := db.Dialect().(Catalog)
	if !ok {
		catalog = StandardCatalog{}
	}
	names := map[string]bool{}
	tables := map[string]string{}
	layers := make([]*layer, 0, len(sets))
	for _, set := range sets {
		if set.Name == "" {
			return nil, errors.New("migrate: a set has no name")
		}
		if names[set.Name] {
			return nil, fmt.Errorf("migrate: set %q is declared twice", set.Name)
		}
		names[set.Name] = true
		table := set.Table
		if table == "" {
			table = DefaultTable
		}
		if !tableName.MatchString(table) {
			return nil, fmt.Errorf("migrate: set %q: table name %q is not a plain identifier", set.Name, table)
		}
		if other, ok := tables[table]; ok {
			return nil, fmt.Errorf("migrate: sets %q and %q share the history table %q", other, set.Name, table)
		}
		tables[table] = set.Name
		if err := validate(set); err != nil {
			return nil, err
		}
		layers = append(layers, &layer{
			name:       set.Name,
			table:      table,
			migrations: copyOf(set.Migrations),
			sql:        history(table, db.Dialect().Placeholder, catalog),
		})
	}
	if opts.LockName == "" {
		opts.LockName = "migrate." + layers[len(layers)-1].table
	}
	return &Migrator{db: db, layers: layers, opts: opts, locker: locker}, nil
}

// validate checks one set's migrations: versions positive and strictly
// increasing, names and up texts present.
func validate(set Set) error {
	last := 0
	for _, mig := range set.Migrations {
		switch {
		case mig.Version <= 0:
			return fmt.Errorf("migrate: set %q: version %d must be positive", set.Name, mig.Version)
		case mig.Version <= last:
			return fmt.Errorf("migrate: set %q: version %d out of order after %d", set.Name, mig.Version, last)
		case mig.Name == "":
			return fmt.Errorf("migrate: set %q: version %d has no name", set.Name, mig.Version)
		case mig.Up == "":
			return fmt.Errorf("migrate: set %q: version %d has no up", set.Name, mig.Version)
		}
		last = mig.Version
	}
	return nil
}

// topLayer is the handle on the last set declared, the one a method without
// a set name acts on.
func (m *Migrator) topLayer() Layer { return Layer{m: m, i: len(m.layers) - 1} }

// Layers returns a handle on every set, in declared order.
func (m *Migrator) Layers() []Layer {
	out := make([]Layer, len(m.layers))
	for i := range m.layers {
		out[i] = Layer{m: m, i: i}
	}
	return out
}

// Layer returns a handle on the set called name, reporting whether the
// migrator holds one.
func (m *Migrator) Layer(name string) (Layer, bool) {
	for i, l := range m.layers {
		if l.name == name {
			return Layer{m: m, i: i}, true
		}
	}
	return Layer{}, false
}

// Migrations returns a copy of the top set's migrations.
func (m *Migrator) Migrations() []Migration { return m.topLayer().Migrations() }

// Version reads the top set's history head without taking the lock. A
// missing history table is the zero Version.
func (m *Migrator) Version(ctx context.Context) (Version, error) { return m.topLayer().Version(ctx) }

// Verify checks every set in declared order, without the lock, that its
// history is a clean, complete prefix of its migrations, and returns the
// first fault as a *SetError naming the set.
func (m *Migrator) Verify(ctx context.Context) error {
	for _, l := range m.layers {
		if err := m.verify(ctx, l); err != nil {
			return err
		}
	}
	return nil
}

// Up applies every pending migration of every set, sets in declared order,
// under one lock on one connection. Every set's history is checked first,
// so a dirty set or a history that does not match its set refuses the run
// before any migration runs. The first failure stops the run; the sets
// before it stay applied.
func (m *Migrator) Up(ctx context.Context) error {
	return m.locked(ctx, func(ctx context.Context, conn *sql.Conn) error {
		applied, err := m.preflight(ctx, conn)
		if err != nil {
			return err
		}
		for i, l := range m.layers {
			if err := m.applyPending(ctx, conn, l, applied[i], len(l.migrations)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Down reverts the top set's n most recently applied migrations. A layer
// above with applied migrations is ErrAboveApplied; Layer.Down reverts one
// named set.
func (m *Migrator) Down(ctx context.Context, n int) error { return m.topLayer().Down(ctx, n) }

// Steps applies the top set's next n pending migrations when n is positive,
// or reverts its last -n applied when negative; fewer remaining is not an
// error.
func (m *Migrator) Steps(ctx context.Context, n int) error { return m.topLayer().Steps(ctx, n) }

// Force sets the top set's history to version as an operator override.
// Layer.Force forces one named set.
func (m *Migrator) Force(ctx context.Context, version int) error {
	return m.topLayer().Force(ctx, version)
}

// Reset returns the database to its state before the first Up: under one
// lock, after every set's history is checked, it reverts every set in
// reverse declared order and drops each set's history table once that set
// is reverted, so a set above never blocks the revert of the set it
// references. A later Up replays every set from zero. A caller that wants
// every set reverted with the tables left in place iterates Layers in
// reverse and calls Down(ctx, len(l.Migrations())) on each.
func (m *Migrator) Reset(ctx context.Context) error {
	return m.locked(ctx, func(ctx context.Context, conn *sql.Conn) error {
		applied, err := m.preflight(ctx, conn)
		if err != nil {
			return err
		}
		for i, l := range slices.Backward(m.layers) {
			if err := m.revertApplied(ctx, conn, l, applied[i], len(applied[i])); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, l.sql.drop); err != nil {
				return &SetError{Set: l.name, Err: m.db.MapError(err)}
			}
			m.log("history table dropped", "set", l.name, "table", l.table)
		}
		return nil
	})
}

// Status reads every set's state in declared order without the lock. It
// runs on the pool, so it never waits on a running Up, and a report read
// while a run is in progress can show a set mid-run.
func (m *Migrator) Status(ctx context.Context) ([]SetStatus, error) {
	out := make([]SetStatus, 0, len(m.layers))
	for _, l := range m.layers {
		s, err := m.status(ctx, l)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// version reads one layer's history head from the pool.
func (m *Migrator) version(ctx context.Context, l *layer) (Version, error) {
	var v Version
	ok, err := m.tableExists(ctx, m.db, l)
	if err != nil {
		return v, &SetError{Set: l.name, Err: err}
	}
	if !ok {
		return v, nil
	}
	found, err := m.queryOne(ctx, m.db, l.sql.head, nil, &v.Version, &v.Dirty)
	if err != nil {
		return Version{}, &SetError{Set: l.name, Err: err}
	}
	if !found {
		return Version{}, nil
	}
	return v, nil
}

// verify checks one layer's history against its migrations, from the pool.
func (m *Migrator) verify(ctx context.Context, l *layer) error {
	applied, err := m.read(ctx, m.db, l)
	if err != nil {
		return &SetError{Set: l.name, Err: err}
	}
	if err := checkPrefix(l.migrations, applied); err != nil {
		return &SetError{Set: l.name, Err: err}
	}
	if len(applied) < len(l.migrations) {
		var pending []int
		for _, mig := range l.migrations[len(applied):] {
			pending = append(pending, mig.Version)
		}
		return &SetError{Set: l.name, Err: &PendingError{Versions: pending}}
	}
	return nil
}

// status reads one layer's state from the pool. Pending migrations and a
// dirty row are state it reports, not failures; a history row the set does
// not carry is an *UnknownVersionError.
func (m *Migrator) status(ctx context.Context, l *layer) (SetStatus, error) {
	applied, err := m.read(ctx, m.db, l)
	if err != nil {
		return SetStatus{}, &SetError{Set: l.name, Err: err}
	}
	s := SetStatus{Name: l.name, Table: l.table}
	for i, r := range applied {
		if i >= len(l.migrations) || l.migrations[i].Version != r.version || l.migrations[i].Name != r.name {
			return SetStatus{}, &SetError{Set: l.name, Err: &UnknownVersionError{Version: r.version, Name: r.name}}
		}
		s.Version = r.version
		if r.dirty {
			s.Dirty = true
		}
	}
	if n := len(l.migrations); n > 0 {
		s.Latest = l.migrations[n-1].Version
	}
	if len(applied) < len(l.migrations) {
		s.Pending = copyOf(l.migrations[len(applied):])
	}
	return s, nil
}

// read returns the layer's history, nil when its table does not exist.
func (m *Migrator) read(ctx context.Context, q querier, l *layer) ([]row, error) {
	ok, err := m.tableExists(ctx, q, l)
	if err != nil || !ok {
		return nil, err
	}
	return m.readHistory(ctx, q, l)
}

// preflight makes sure every set's history table exists and reads and
// checks every set's history on the pinned connection, in declared order,
// before any set runs. It runs inside the lock, so no starter can change a
// history between the check and the run.
func (m *Migrator) preflight(ctx context.Context, conn *sql.Conn) ([][]row, error) {
	applied := make([][]row, len(m.layers))
	for i, l := range m.layers {
		if _, err := conn.ExecContext(ctx, l.sql.create); err != nil {
			return nil, &SetError{Set: l.name, Err: m.db.MapError(err)}
		}
		rows, err := m.readHistory(ctx, conn, l)
		if err != nil {
			return nil, &SetError{Set: l.name, Err: err}
		}
		if err := checkPrefix(l.migrations, rows); err != nil {
			return nil, &SetError{Set: l.name, Err: err}
		}
		applied[i] = rows
	}
	return applied, nil
}

// aboveApplied refuses a revert of layer i while a layer above it still has
// applied migrations.
func (m *Migrator) aboveApplied(applied [][]row, i int) error {
	for j := i + 1; j < len(m.layers); j++ {
		if len(applied[j]) > 0 {
			return fmt.Errorf("%w: %q above %q", ErrAboveApplied, m.layers[j].name, m.layers[i].name)
		}
	}
	return nil
}

// belowPending refuses an apply on layer i while a layer below it still has
// pending migrations.
func (m *Migrator) belowPending(applied [][]row, i int) error {
	for j := range i {
		if len(applied[j]) < len(m.layers[j].migrations) {
			return fmt.Errorf("%w: %q below %q", ErrBelowPending, m.layers[j].name, m.layers[i].name)
		}
	}
	return nil
}

// applyPending applies up to n of the layer's pending migrations, in
// version order.
func (m *Migrator) applyPending(ctx context.Context, conn *sql.Conn, l *layer, applied []row, n int) error {
	pending := l.migrations[len(applied):]
	if n < len(pending) {
		pending = pending[:n]
	}
	for _, mig := range pending {
		if err := m.applyOne(ctx, conn, l, mig); err != nil {
			return &SetError{Set: l.name, Err: err}
		}
	}
	return nil
}

// revertApplied reverts up to n of the layer's applied migrations, most
// recent first.
func (m *Migrator) revertApplied(ctx context.Context, conn *sql.Conn, l *layer, applied []row, n int) error {
	for i := 0; i < n && len(applied)-1-i >= 0; i++ {
		mig := l.migrations[len(applied)-1-i]
		if err := m.revertOne(ctx, conn, l, mig); err != nil {
			return &SetError{Set: l.name, Err: err}
		}
	}
	return nil
}

// locked pins a connection, takes the lock when the dialect has one, runs
// fn, and releases the lock under a context that survives cancellation,
// before the connection returns to the pool. Once ctx has ended the driver
// has discarded the connection and the session's end releases the lock, so
// an unlock failure then is noise and is not reported. A dialect without
// the capability is ErrNoLocker unless Unlocked. The history tables are
// not created here: preflight creates every set's table, and Force creates
// only its own set's.
func (m *Migrator) locked(ctx context.Context, fn func(context.Context, *sql.Conn) error) (err error) {
	if m.locker == nil && !m.opts.Unlocked {
		return ErrNoLocker
	}
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil && err == nil {
			err = m.db.MapError(cerr)
		}
	}()
	if m.locker != nil && !m.opts.Unlocked {
		if err := m.locker.Lock(ctx, conn, m.opts.LockName); err != nil {
			return m.db.MapError(err)
		}
		defer func() {
			if uerr := m.locker.Unlock(context.WithoutCancel(ctx), conn, m.opts.LockName); uerr != nil && ctx.Err() == nil {
				err = errors.Join(err, m.db.MapError(uerr))
			}
		}()
	}
	return fn(ctx, conn)
}

// applyOne runs one migration of one layer: in a transaction with its
// history insert, so a failure records nothing; or, when the migration opts
// out, as a dirty insert, the statement, then the clean-up, so a failure
// leaves the row dirty and every later run refuses until Force.
func (m *Migrator) applyOne(ctx context.Context, conn *sql.Conn, l *layer, mig Migration) error {
	if mig.Transactional {
		err := m.inTx(ctx, conn, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, mig.Up); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, l.sql.insert, mig.Version, mig.Name, false)
			return err
		})
		if err != nil {
			return fmt.Errorf("migrate: apply %d %s: %w", mig.Version, mig.Name, err)
		}
		m.log("migration applied", "set", l.name, "version", mig.Version, "name", mig.Name)
		return nil
	}
	if _, err := conn.ExecContext(ctx, l.sql.insert, mig.Version, mig.Name, true); err != nil {
		return fmt.Errorf("migrate: apply %d %s: %w", mig.Version, mig.Name, m.db.MapError(err))
	}
	if _, err := conn.ExecContext(ctx, mig.Up); err != nil {
		return &DirtyError{Version: mig.Version, Err: m.db.MapError(err)}
	}
	if _, err := conn.ExecContext(ctx, l.sql.setDirty, false, mig.Version); err != nil {
		return &DirtyError{Version: mig.Version, Err: m.db.MapError(err)}
	}
	m.log("migration applied", "set", l.name, "version", mig.Version, "name", mig.Name, "transactional", false)
	return nil
}

// revertOne runs one migration's down with the symmetric history change.
func (m *Migrator) revertOne(ctx context.Context, conn *sql.Conn, l *layer, mig Migration) error {
	if mig.Down == "" {
		return fmt.Errorf("%w: %d %s", ErrNoDown, mig.Version, mig.Name)
	}
	if mig.Transactional {
		err := m.inTx(ctx, conn, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, mig.Down); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, l.sql.del, mig.Version)
			return err
		})
		if err != nil {
			return fmt.Errorf("migrate: revert %d %s: %w", mig.Version, mig.Name, err)
		}
		m.log("migration reverted", "set", l.name, "version", mig.Version, "name", mig.Name)
		return nil
	}
	if _, err := conn.ExecContext(ctx, l.sql.setDirty, true, mig.Version); err != nil {
		return fmt.Errorf("migrate: revert %d %s: %w", mig.Version, mig.Name, m.db.MapError(err))
	}
	if _, err := conn.ExecContext(ctx, mig.Down); err != nil {
		return &DirtyError{Version: mig.Version, Err: m.db.MapError(err)}
	}
	if _, err := conn.ExecContext(ctx, l.sql.del, mig.Version); err != nil {
		return &DirtyError{Version: mig.Version, Err: m.db.MapError(err)}
	}
	m.log("migration reverted", "set", l.name, "version", mig.Version, "name", mig.Name, "transactional", false)
	return nil
}

// inTx runs fn in a transaction on the pinned connection, rolling back on
// error and mapping the failure. Once ctx has ended, database/sql rolls the
// transaction back itself and the driver discards the connection, so the
// explicit rollback's result reports nothing new and is not joined; nor is
// ErrTxDone, the same fact reported when that rollback runs first.
func (m *Migrator) inTx(ctx context.Context, conn *sql.Conn, fn func(*sql.Tx) error) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return m.db.MapError(err)
	}
	if err := fn(tx); err != nil {
		err = m.db.MapError(err)
		if rbErr := tx.Rollback(); rbErr != nil && ctx.Err() == nil && !errors.Is(rbErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}
	return m.db.MapError(tx.Commit())
}

type row struct {
	version int
	name    string
	dirty   bool
}

// querier is the read half of the session, satisfied by *sqlate.DB and by a
// pinned *sql.Conn. Single-row reads go through QueryContext because the
// Session interface deliberately leaves QueryRowContext out: *sql.Row
// defers its error to Scan, where nothing can map it.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// queryOne scans the first row into dest, reporting whether there was one.
func (m *Migrator) queryOne(ctx context.Context, q querier, query string, args []any, dest ...any) (bool, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return false, m.db.MapError(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return false, m.db.MapError(rows.Err())
	}
	if err := rows.Scan(dest...); err != nil {
		return false, m.db.MapError(err)
	}
	return true, m.db.MapError(rows.Err())
}

func (m *Migrator) tableExists(ctx context.Context, q querier, l *layer) (bool, error) {
	var n int
	if _, err := m.queryOne(ctx, q, l.sql.exists, []any{l.table}, &n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (m *Migrator) readHistory(ctx context.Context, q querier, l *layer) ([]row, error) {
	rows, err := q.QueryContext(ctx, l.sql.all)
	if err != nil {
		return nil, m.db.MapError(err)
	}
	defer func() { _ = rows.Close() }()
	var out []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.version, &r.name, &r.dirty); err != nil {
			return nil, m.db.MapError(err)
		}
		out = append(out, r)
	}
	return out, m.db.MapError(rows.Err())
}

// checkPrefix refuses a dirty row and requires the applied rows to be the
// migrations' prefix, by version and name.
func checkPrefix(migrations []Migration, applied []row) error {
	for i, r := range applied {
		if r.dirty {
			return &DirtyError{Version: r.version}
		}
		if i >= len(migrations) || migrations[i].Version != r.version || migrations[i].Name != r.name {
			return &UnknownVersionError{Version: r.version, Name: r.name}
		}
	}
	return nil
}

func (m *Migrator) log(msg string, args ...any) {
	if m.opts.Logger != nil {
		m.opts.Logger.Info(msg, args...)
	}
}
