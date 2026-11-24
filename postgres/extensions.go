package postgres

import "github.com/jackc/pgx/v5"

// ExtensionState returns the read of one extension's state from
// pg_available_extensions, the extension's name bound at param: its
// installed and default versions, and whether pg_extension_update_paths
// lists a path from the installed version to the default. It yields no row
// for an extension the server does not offer.
func (Dialect) ExtensionState(param string) string {
	return "SELECT a.installed_version, a.default_version, " +
		"CASE WHEN a.installed_version IS NULL THEN FALSE " +
		"WHEN a.installed_version = a.default_version THEN TRUE " +
		"ELSE EXISTS (SELECT 1 FROM pg_extension_update_paths(a.name) p " +
		"WHERE p.source = a.installed_version AND p.target = a.default_version AND p.path IS NOT NULL) END " +
		"FROM pg_available_extensions a WHERE a.name = " + param
}

// ActivateExtension returns CREATE EXTENSION IF NOT EXISTS, which installs
// the default version, then ALTER EXTENSION UPDATE, which moves an
// installed extension to the default. The name is quoted as an identifier.
func (Dialect) ActivateExtension(name string) []string {
	ident := pgx.Identifier{name}.Sanitize()
	return []string{
		"CREATE EXTENSION IF NOT EXISTS " + ident,
		"ALTER EXTENSION " + ident + " UPDATE",
	}
}

// RemoveExtension returns DROP EXTENSION IF EXISTS with RESTRICT, which the
// server refuses with SQLSTATE 2BP01, sqlate.ErrDependentObjects through
// MapError, while an object depends on the extension. The name is quoted as
// an identifier.
func (Dialect) RemoveExtension(name string) string {
	return "DROP EXTENSION IF EXISTS " + pgx.Identifier{name}.Sanitize() + " RESTRICT"
}
