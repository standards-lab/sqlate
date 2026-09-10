// Package sqlint checks the authored SQL files of a module against the
// conventions the loader cannot, or should not, enforce at runtime:
//
//   - every statement directory compiles against the pattern sources the
//     runtime registers (the header grammar, the parameter syntax, the
//     field contract, includes resolving)
//   - every pattern directory validates as a catalog source
//   - a file is named for its operation and not its SQL verb
//   - the parameter delimiter does not appear inside a comment or a string
//     literal
//   - a standard-tier file uses no native form the configured engine
//     declares
//   - a migration headed "transaction: none" contains exactly one statement
//
// It is a sub-module of sqlate so the TOML parser it sources
// enters a build only through this import; cmd/sqlint is its command, and
// a harness calls the package.
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
// what a consumer reads: the directory its patterns publish, the overlay
// directory an engine supplies, and the native forms an engine names, each
// a regular expression under the name a finding reports. A bare directory
// is the pattern files themselves, the module's own. Absent the file, the
// roles are the conventions as they stand, every check on, and no source
// is registered.
//
// # Running
//
// [Lint] walks a filesystem under a [Config] and returns every [Finding]:
// the file, the line when the check has one, and the message. Errors in
// the configuration's own syntax are [Load]'s; a source or engine that
// does not resolve is a finding against sqlint.toml.
package sqlint
