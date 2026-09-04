package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/standards-lab/sqlate"
)

// Options configures a Migrator. Every field has a default.
type Options struct {
	// Table names the history table; default "schema_version".
	Table string
	// LockName names the lock a run holds; default "migrate.<table>", so two
	// migrators over different tables never contend and no domain lock can
	// collide with it by number.
	LockName string
	// Unlocked allows runs on a dialect without the lock capability, or with
	// it, without taking the lock; concurrent starters are then unsafe.
	Unlocked bool
	// Logger records each applied and reverted migration; nil is silent.
	Logger *slog.Logger
}

// Version is the history's head: the highest applied version and whether
// its row is dirty. A zero Version means nothing is applied.
type Version struct {
	Version int
	Dirty   bool
}

// Migrator runs a migration set against a database.
type Migrator struct {
	db         *sqlate.DB
	migrations []Migration
	opts       Options
	locker     sqlate.Locker
	sql        statements
}

var tableName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// New validates the set — versions positive and strictly increasing, names
// and up texts present — and prepares the migrator: the lock capability and
// the Catalog are taken from the dialect when it has them. It performs no
// I/O.
func New(db *sqlate.DB, migrations []Migration, opts Options) (*Migrator, error) {
	if db == nil {
		return nil, errors.New("migrate: nil db")
	}
	if opts.Table == "" {
		opts.Table = "schema_version"
	}
	if !tableName.MatchString(opts.Table) {
		return nil, fmt.Errorf("migrate: table name %q is not a plain identifier", opts.Table)
	}
	if opts.LockName == "" {
		opts.LockName = "migrate." + opts.Table
	}
	last := 0
	for _, m := range migrations {
		switch {
		case m.Version <= 0:
			return nil, fmt.Errorf("migrate: version %d must be positive", m.Version)
		case m.Version <= last:
			return nil, fmt.Errorf("migrate: version %d out of order after %d", m.Version, last)
		case m.Name == "":
			return nil, fmt.Errorf("migrate: version %d has no name", m.Version)
		case m.Up == "":
			return nil, fmt.Errorf("migrate: version %d has no up", m.Version)
		}
		last = m.Version
	}
	set := make([]Migration, len(migrations))
	copy(set, migrations)
	locker, _ := db.Dialect().(sqlate.Locker)
	catalog, ok := db.Dialect().(Catalog)
	if !ok {
		catalog = StandardCatalog{}
	}
	return &Migrator{
		db:         db,
		migrations: set,
		opts:       opts,
		locker:     locker,
		sql:        history(opts.Table, db.Dialect().Placeholder, catalog),
	}, nil
}

// Migrations returns a copy of the set.
func (m *Migrator) Migrations() []Migration {
	out := make([]Migration, len(m.migrations))
	copy(out, m.migrations)
	return out
}

// Version reads the history's head without taking the lock. A missing
// history table is the zero Version.
func (m *Migrator) Version(ctx context.Context) (Version, error) {
	var v Version
	ok, err := m.tableExists(ctx, m.db)
	if err != nil || !ok {
		return v, err
	}
	found, err := m.queryOne(ctx, m.db, m.sql.head, nil, &v.Version, &v.Dirty)
	if err != nil || !found {
		return Version{}, err
	}
	return v, nil
}

// Verify checks, without the lock, that the history is a clean, complete
// prefix of the set: a dirty row is a *DirtyError, a row the set does not
// carry is an *UnknownVersionError, and unapplied migrations are a
// *PendingError.
func (m *Migrator) Verify(ctx context.Context) error {
	ok, err := m.tableExists(ctx, m.db)
	if err != nil {
		return err
	}
	var applied []row
	if ok {
		applied, err = m.readHistory(ctx, m.db)
		if err != nil {
			return err
		}
	}
	if err := m.checkPrefix(applied); err != nil {
		return err
	}
	if len(applied) < len(m.migrations) {
		var pending []int
		for _, mig := range m.migrations[len(applied):] {
			pending = append(pending, mig.Version)
		}
		return &PendingError{Versions: pending}
	}
	return nil
}

// Up applies every pending migration.
func (m *Migrator) Up(ctx context.Context) error {
	return m.Steps(ctx, len(m.migrations))
}

// Down reverts the n most recently applied migrations.
func (m *Migrator) Down(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	return m.Steps(ctx, -n)
}

// Steps applies the next n pending migrations when n is positive, or reverts
// the last -n applied when negative; fewer remaining is not an error.
func (m *Migrator) Steps(ctx context.Context, n int) error {
	if n == 0 {
		return nil
	}
	return m.locked(ctx, func(ctx context.Context, conn *sql.Conn) error {
		applied, err := m.readHistory(ctx, conn)
		if err != nil {
			return err
		}
		if err := m.checkPrefix(applied); err != nil {
			return err
		}
		if n > 0 {
			pending := m.migrations[len(applied):]
			if n < len(pending) {
				pending = pending[:n]
			}
			for _, mig := range pending {
				if err := m.apply(ctx, conn, mig); err != nil {
					return err
				}
			}
			return nil
		}
		for i := 0; i < -n && len(applied)-1-i >= 0; i++ {
			mig := m.migrations[len(applied)-1-i]
			if err := m.revert(ctx, conn, mig); err != nil {
				return err
			}
		}
		return nil
	})
}

// Force sets the history to version as an operator override: rows above it
// are deleted, its row is inserted if absent and marked clean, and version 0
// empties the history. Nothing runs against the schema itself.
func (m *Migrator) Force(ctx context.Context, version int) error {
	var name string
	if version != 0 {
		found := false
		for _, mig := range m.migrations {
			if mig.Version == version {
				name, found = mig.Name, true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: %d", ErrVersionNotFound, version)
		}
	}
	return m.locked(ctx, func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, m.sql.delAbove, version); err != nil {
			return m.db.MapError(err)
		}
		if version == 0 {
			return nil
		}
		res, err := conn.ExecContext(ctx, m.sql.setDirty, false, version)
		if err != nil {
			return m.db.MapError(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err := conn.ExecContext(ctx, m.sql.insert, version, name, false); err != nil {
				return m.db.MapError(err)
			}
		}
		m.log("migration forced", "version", version)
		return nil
	})
}

// locked pins a connection, takes the lock when the dialect has one, makes
// sure the history table exists, runs fn, and releases the lock under a
// context that survives cancellation, before the connection returns to the
// pool. Once ctx has ended the driver has discarded the connection and the
// session's end releases the lock, so an unlock failure then is noise and
// is not reported. A dialect without the capability is ErrNoLocker unless
// Unlocked.
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
	if _, err := conn.ExecContext(ctx, m.sql.create); err != nil {
		return m.db.MapError(err)
	}
	return fn(ctx, conn)
}

// apply runs one migration: in a transaction with its history insert, so a
// failure records nothing; or, when the migration opts out, as a dirty
// insert, the statement, then the clean-up, so a failure leaves the row
// dirty and every later run refuses until Force.
func (m *Migrator) apply(ctx context.Context, conn *sql.Conn, mig Migration) error {
	if mig.Transactional {
		err := m.inTx(ctx, conn, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, mig.Up); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, m.sql.insert, mig.Version, mig.Name, false)
			return err
		})
		if err != nil {
			return fmt.Errorf("migrate: apply %d %s: %w", mig.Version, mig.Name, err)
		}
		m.log("migration applied", "version", mig.Version, "name", mig.Name)
		return nil
	}
	if _, err := conn.ExecContext(ctx, m.sql.insert, mig.Version, mig.Name, true); err != nil {
		return fmt.Errorf("migrate: apply %d %s: %w", mig.Version, mig.Name, m.db.MapError(err))
	}
	if _, err := conn.ExecContext(ctx, mig.Up); err != nil {
		return &DirtyError{Version: mig.Version, Err: m.db.MapError(err)}
	}
	if _, err := conn.ExecContext(ctx, m.sql.setDirty, false, mig.Version); err != nil {
		return &DirtyError{Version: mig.Version, Err: m.db.MapError(err)}
	}
	m.log("migration applied", "version", mig.Version, "name", mig.Name, "transactional", false)
	return nil
}

// revert runs one migration's down with the symmetric history change.
func (m *Migrator) revert(ctx context.Context, conn *sql.Conn, mig Migration) error {
	if mig.Down == "" {
		return fmt.Errorf("%w: %d %s", ErrNoDown, mig.Version, mig.Name)
	}
	if mig.Transactional {
		err := m.inTx(ctx, conn, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, mig.Down); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, m.sql.del, mig.Version)
			return err
		})
		if err != nil {
			return fmt.Errorf("migrate: revert %d %s: %w", mig.Version, mig.Name, err)
		}
		m.log("migration reverted", "version", mig.Version, "name", mig.Name)
		return nil
	}
	if _, err := conn.ExecContext(ctx, m.sql.setDirty, true, mig.Version); err != nil {
		return fmt.Errorf("migrate: revert %d %s: %w", mig.Version, mig.Name, m.db.MapError(err))
	}
	if _, err := conn.ExecContext(ctx, mig.Down); err != nil {
		return &DirtyError{Version: mig.Version, Err: m.db.MapError(err)}
	}
	if _, err := conn.ExecContext(ctx, m.sql.del, mig.Version); err != nil {
		return &DirtyError{Version: mig.Version, Err: m.db.MapError(err)}
	}
	m.log("migration reverted", "version", mig.Version, "name", mig.Name, "transactional", false)
	return nil
}

// inTx runs fn in a transaction on the pinned connection, rolling back on
// error and mapping the failure. Once ctx has ended, database/sql rolls the
// transaction back itself and the driver discards the connection, so the
// explicit rollback's result carries nothing and is not joined; nor is
// ErrTxDone, the same fact reported when that rollback lands first.
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
// seam deliberately leaves QueryRowContext out: *sql.Row defers its error to
// Scan, where nothing can map it.
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

func (m *Migrator) tableExists(ctx context.Context, q querier) (bool, error) {
	var n int
	if _, err := m.queryOne(ctx, q, m.sql.exists, []any{m.opts.Table}, &n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (m *Migrator) readHistory(ctx context.Context, q querier) ([]row, error) {
	rows, err := q.QueryContext(ctx, m.sql.all)
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
// set's prefix, by version and name.
func (m *Migrator) checkPrefix(applied []row) error {
	for i, r := range applied {
		if r.dirty {
			return &DirtyError{Version: r.version}
		}
		if i >= len(m.migrations) || m.migrations[i].Version != r.version || m.migrations[i].Name != r.name {
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
