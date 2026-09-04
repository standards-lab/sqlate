package migrate

// Catalog is the engine-specific half of the history protocol: the two
// statements standard SQL cannot express the same way on every engine. A
// dialect provides it by implementing the methods; a dialect that does not
// gets StandardCatalog. Everything else the migrator runs is standard DML
// with bound parameters.
type Catalog interface {
	// CreateHistory returns the DDL that creates table when it does not
	// exist, with the columns the protocol reads and writes: version
	// (integer, primary key), name (text, not null), applied_at (timestamp,
	// not null, defaulting to the current time), dirty (boolean, not null,
	// defaulting to false).
	CreateHistory(table string) string
	// HistoryExists returns the query that yields one row whose first
	// column is nonzero when the history table exists. The table name is
	// the query's one argument, bound at param, the dialect's placeholder.
	HistoryExists(param string) string
}

// StandardCatalog is the Catalog for engines that accept CREATE TABLE IF NOT
// EXISTS with text and boolean columns and expose information_schema:
// PostgreSQL, MySQL, and MariaDB as they ship. SQL Server (no IF NOT EXISTS,
// no boolean), Oracle (no information_schema), and SQLite (sqlite_master)
// provide their own.
type StandardCatalog struct{}

var _ Catalog = StandardCatalog{}

func (StandardCatalog) CreateHistory(table string) string {
	return "CREATE TABLE IF NOT EXISTS " + table + " (" +
		"version integer PRIMARY KEY, " +
		"name text NOT NULL, " +
		"applied_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP, " +
		"dirty boolean NOT NULL DEFAULT FALSE)"
}

func (StandardCatalog) HistoryExists(param string) string {
	return "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = " + param
}

// statements are the texts one migrator runs, rendered once with the
// dialect's placeholders: the catalog pair from the Catalog, the rest
// standard DML. Booleans travel as parameters, never as literals.
type statements struct {
	create, exists, all, head, insert, setDirty, del, delAbove string
}

// history renders the statements for the history table t over
// catalog c, with p as the dialect's placeholder.
func history(t string, p func(int) string, c Catalog) statements {
	return statements{
		create:   c.CreateHistory(t),
		exists:   c.HistoryExists(p(1)),
		all:      "SELECT version, name, dirty FROM " + t + " ORDER BY version",
		head:     "SELECT version, dirty FROM " + t + " WHERE version = (SELECT MAX(version) FROM " + t + ")",
		insert:   "INSERT INTO " + t + " (version, name, dirty) VALUES (" + p(1) + ", " + p(2) + ", " + p(3) + ")",
		setDirty: "UPDATE " + t + " SET dirty = " + p(1) + " WHERE version = " + p(2),
		del:      "DELETE FROM " + t + " WHERE version = " + p(1),
		delAbove: "DELETE FROM " + t + " WHERE version > " + p(1),
	}
}
