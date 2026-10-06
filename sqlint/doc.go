// Package sqlint checks the authored SQL files of a module against the
// conventions the loader cannot, or should not, enforce at runtime:
//
//   - every statement directory compiles against the pattern sources the
//     runtime registers (the header grammar, the parameter syntax, the
//     field contract, includes resolving, a returning command's read)
//   - every pattern directory validates as a catalog source
//   - a file is named for its operation and not its SQL verb
//   - the parameter delimiter does not appear inside a comment or a string
//     literal
//   - a standard-tier file uses no native form the configured engine
//     declares
//   - a statement that includes one of the guard protocol's patterns
//     includes both: guard_set beside guard_where in a SET statement, and
//     guard_where beside guard_set
//   - a migration headed "transaction: none" contains exactly one statement
//
// It is a sub-module of sqlate so the TOML parser it sources
// enters a build only through this import; cmd/sqlint is its command, and
// a harness calls the package.
//
// The package exports:
//
//   - [Load], which reads [File] into a [Config]
//   - [Config], the parsed configuration, with its [Role] per role, its
//     [Source] per namespace, and its [Export] for a consumer
//   - [Lint], which walks a filesystem and returns every [Finding]
//   - [Resolver], which turns a module path into its filesystem, and
//     [GoList], the one go list backs
//
// # Configuration
//
// sqlint.toml at the module root configures the linter, one file per module,
// read by [Load]:
//
//   - a table per role (statements, patterns, migrations) with the
//     directory globs it covers and the switches of its checks
//   - an override table per directory set that needs an exception
//   - the pattern sources by namespace
//   - the engine
//
// A source or the engine is a path:
// a directory of the tree, or a module path resolved through the
// [Resolver] to the version go.mod pins. A producer, a module or a
// directory that contains its own sqlint.toml, declares in its [export] table
// what a consumer reads, as [Export] states it. A bare directory
// is the pattern files themselves, the module's own. Absent the file, the
// roles are the conventions as they stand, every check on, and no source
// is registered.
package sqlint
