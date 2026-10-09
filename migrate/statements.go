package migrate

// Catalog is the engine-specific half of the history protocol: the two
// statements standard SQL cannot express the same way on every engine. A
// dialect provides it by implementing the methods; a dialect that does not
// gets StandardCatalog. Everything else the migrator runs is standard DML
// with bound parameters.
type Catalog interface {
	// CreateHistory returns the DDL that creates table when it does not
	// exist, with the columns the protocol reads and writes:
	//
	//   - version (integer, primary key)
	//   - name (text, not null)
	//   - applied_at (the instant the row was written, not null,
	//     defaulting to the current time; a time-zone-aware type where the
	//     engine has one, so the instant does not depend on the session's
	//     zone)
	//   - dirty (boolean, not null, defaulting to false)
	CreateHistory(table string) string
	// HistoryExists returns the query that yields one row whose first
	// column is nonzero when the history table exists. The table name is
	// the query's one argument, bound at param, the dialect's placeholder.
	HistoryExists(param string) string
}

// HistoryUpgrader is the Catalog capability that brings a history table an
// earlier release created to the shape CreateHistory creates now. A dialect
// provides it by implementing the two methods alongside Catalog's. Each
// locked run (Up, Steps, Down, Reset, Force) runs HistoryOutdated after
// CreateHistory, inside the lock and before it reads the history, and runs
// UpgradeHistory only when the query reports the table outdated, so a
// current table is read and never rewritten, and the upgrade is idempotent
// however the engine's DDL behaves on a second run.
type HistoryUpgrader interface {
	// HistoryOutdated returns the query that yields one row whose first
	// column is nonzero when the history table has an earlier release's
	// shape. The table name is the query's one argument, bound at param,
	// the dialect's placeholder.
	HistoryOutdated(param string) string
	// UpgradeHistory returns the DDL that converts table to the current
	// shape in place, keeping every row and the instant each records.
	UpgradeHistory(table string) string
}

// StandardCatalog is the Catalog for engines that accept CREATE TABLE IF NOT
// EXISTS with text and boolean columns and expose information_schema:
// MySQL and MariaDB as they ship, and PostgreSQL as a fallback for a dialect
// that does not implement its own (see HistoryExists's limitation). SQL
// Server (no IF NOT EXISTS, no boolean), Oracle (no information_schema),
// and SQLite (sqlite_master) provide their own.
//
// Its applied_at is a plain timestamp, the one spelling all its engines
// accept. On MySQL and MariaDB a timestamp is stored as an instant, so the
// column is time-zone-aware there; on PostgreSQL it is a wall clock without
// zone, which is one more reason the postgres dialect implements its own
// Catalog, with timestamp with time zone. StandardCatalog has no
// HistoryUpgrader, since there is nothing for its engines to upgrade.
type StandardCatalog struct{}

var _ Catalog = StandardCatalog{}

func (StandardCatalog) CreateHistory(table string) string {
	return "CREATE TABLE IF NOT EXISTS " + table + " (" +
		"version integer PRIMARY KEY, " +
		"name text NOT NULL, " +
		"applied_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP, " +
		"dirty boolean NOT NULL DEFAULT FALSE)"
}

// HistoryExists checks information_schema.tables by name alone, which spans
// every schema on the search path: a same-named table in an unrelated
// schema satisfies the check even though the current schema's own history
// table does not exist. A dialect for an engine where this matters — as
// PostgreSQL's own Dialect does — implements Catalog itself with the
// engine's own schema-qualified form instead of falling back to this one.
func (StandardCatalog) HistoryExists(param string) string {
	return "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = " + param
}

// statements are the texts one set's layer runs against its history table,
// rendered once with the dialect's placeholders: the catalog pair from the
// Catalog, the outdated check and its upgrade from a HistoryUpgrader (both
// empty for a catalog without one), the rest standard DML, and drop, the
// DDL Reset runs to remove the set's own history table once that set is
// reverted. Booleans bind as parameters, never as literals.
type statements struct {
	create, exists, outdated, upgrade, all, head, insert, setDirty, del, delAbove, drop string
}

// history renders the statements for the history table t over
// catalog c, with p as the dialect's placeholder.
func history(t string, p func(int) string, c Catalog) statements {
	var outdated, upgrade string
	if u, ok := c.(HistoryUpgrader); ok {
		outdated, upgrade = u.HistoryOutdated(p(1)), u.UpgradeHistory(t)
	}
	return statements{
		outdated: outdated,
		upgrade:  upgrade,
		create:   c.CreateHistory(t),
		exists:   c.HistoryExists(p(1)),
		all:      "SELECT version, name, dirty FROM " + t + " ORDER BY version",
		head:     "SELECT version, dirty FROM " + t + " WHERE version = (SELECT MAX(version) FROM " + t + ")",
		insert:   "INSERT INTO " + t + " (version, name, dirty) VALUES (" + p(1) + ", " + p(2) + ", " + p(3) + ")",
		setDirty: "UPDATE " + t + " SET dirty = " + p(1) + " WHERE version = " + p(2),
		del:      "DELETE FROM " + t + " WHERE version = " + p(1),
		delAbove: "DELETE FROM " + t + " WHERE version > " + p(1),
		drop:     "DROP TABLE " + t,
	}
}
