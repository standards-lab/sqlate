// Package header reads the declaration header of an authored SQL file: the
// leading run of blank lines, plain "--" comments (prose, skipped), and
// "--|" declaration lines of the form "--| key: value", ending at the first
// line that is none of those. The marker makes a declaration definite: a
// "--|" line that is not a declaration is an error, and a plain comment is
// never one. The header is the loader's; End marks where the body the
// engine receives begins. The package knows no keys. Each consumer — query
// for tier, native, transaction, key, and field; migrate for transaction;
// sqlint for each role's checks — decides which keys it accepts and what
// their values mean.
package header
