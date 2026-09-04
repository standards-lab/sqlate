// Package sqltest is the scripted database/sql driver a consumer's hermetic
// tests run over: a connector whose connections consume scripted responses
// in call order and record every statement, prepare, and transaction call
// they receive. It supports prepare, so prepare-based verification has a
// harness, and it accepts any argument value unconverted, so tests see the
// exact Go values a runner bound. It is public because every consumer's
// unit tier runs over it, the library's own packages included.
//
// It is strict where a real driver is, so a test cannot pass on a path
// production would reject: a statement with no response scripted for it
// fails (ErrUnscripted), an argument count that does not match the
// statement's $N placeholders fails (ErrArguments), and a scripted response
// that does not fit its call (rows for an exec, an affected count for a
// query, a row of the wrong width or containing a non-driver.Value) fails
// (ErrScript). The one leniency it keeps is the argument set: a real driver
// rejects a value it cannot encode, and this one records it.
//
// [Open] returns the pool and its [Recorder]; [Dialect] is the stub dialect
// whose MapError wraps every error in a [MappedError], so a test proves
// with one errors.As that an error passed through MapError.
package sqltest
