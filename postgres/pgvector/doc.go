// Package pgvector names what sqlate needs to know of pgvector, the
// PostgreSQL extension for vector similarity search:
//
//   - [Extension], the extension's catalog name, the name an application
//     declares to the migrator and CREATE EXTENSION installs
//   - [Metric], one distance metric of the vector type: its name, its
//     distance operator, and its index operator class
//   - [L2], [Cosine], and [InnerProduct], the three metrics
//   - [Metrics], the metric table, in that order
//
// The table covers the vector type only. The package imports only the
// standard library, so a module that builds queries over pgvector depends
// on the names here without depending on a driver.
//
// # Native forms
//
// The sqlint.toml beside this file exports pgvector's native forms: its
// distance operators, its types where a type goes, its index methods, its
// functions, and its settings, each a regular expression under the name a
// finding reports. A consumer lists this module under sqlint's extensions
// to deny them in standard-tier files.
package pgvector
