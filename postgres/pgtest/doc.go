// Package pgtest gives an integration test its own throwaway PostgreSQL
// database: created on the server an environment variable names, reached
// over pgx's database/sql driver, and dropped when the test ends, with
// FORCE so a session still open on it, such as a service process's pool,
// does not hold the drop back.
//
// The package exports:
//
//   - [Database], one test's database: the pool over it and its DSN
//   - [Create], which creates <prefix>_test_<hex> on the server whose admin
//     DSN the named environment variable holds, failing the test when the
//     variable is unset
//   - [Migrated], which is Create with migration sets applied through the
//     migrate package under the PostgreSQL dialect
//   - [Database.Session], which wraps the pool under postgres.Dialect
//
// It imports testing, so it belongs only in a consumer's test builds: a
// test file imports it, and no production package should.
package pgtest
