// Package header reads the declaration header of an authored SQL file: the
// "--|" declarations that precede the body the engine receives. The header
// is the loader's, never sent. The package knows no keys; each consumer
// decides which keys it accepts and what their values mean. query reads
// tier, native, transaction, key, and field; migrate reads transaction;
// sqlint reads what each role's checks need.
//
// The package exports:
//
//   - [Marker], the prefix that opens a declaration line
//   - [Parse], which reads a file's header
//   - [Header], one file's declarations and where its body begins
//   - [Declaration], one declaration and the line that opens it
package header
