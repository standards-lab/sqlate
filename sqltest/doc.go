// Package sqltest is the scripted database/sql driver a consumer's hermetic
// tests run over: its connections consume scripted responses in call order
// and record every statement, prepare, and transaction call they receive.
// It supports prepare, so prepare-based verification has a harness, and it
// accepts any argument value unconverted, so tests see the exact Go values a
// runner bound. It is public because every consumer's unit tier runs over
// it, the library's own packages included.
//
// It is strict where a real driver is, so a test cannot pass on a path
// production would reject; each refusal is one of its own errors. The one
// leniency it keeps is the argument set: a real driver rejects a value it
// cannot encode, and this one records it.
//
// The package exports:
//
//   - [Open], which returns a pool over a fresh [Recorder]
//   - [Recorder], the script, the call log, and the failure switches behind
//     the pool
//   - [Response], the scripted outcome of one exec or query call
//   - [WithTotal], which scripts a counted collection page
//   - [Call], one recorded driver call, and [Op] with its constants
//     [OpExec], [OpQuery], [OpPrepare], [OpBegin], [OpCommit], and
//     [OpRollback], the call's kind
//   - [ErrUnscripted], [ErrArguments], and [ErrScript], the driver's own
//     failures
//   - [Dialect], the stub dialect, and [MappedError], the mark its MapError
//     leaves on an error
//   - [ReturningDialect], the stub dialect with the query.Returner capability
package sqltest
