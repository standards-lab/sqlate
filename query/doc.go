// Package query runs authored SQL files. A program compiles a directory of
// statements once, where it starts, against a catalog of pattern sources
// (the library's own, an application's, an engine's overlay), fetches each
// statement by its file name, and binds it to the typed handle that runs it.
// Each file's "--|" header declares its tier, the engine feature a native
// file uses, whether it needs a transaction, for a projection base its key
// and field contract, and, for an INSERT INTO or UPDATE, the read that
// returns its changed row. The engine receives the body: the header is the
// loader's.
//
// A statement includes a pattern with {{> ns.name}}. Parameters, written
// {{name}} or {{name:type}} (the type binding through CAST, verbatim),
// resolve to the dialect's positions once at load; arguments bind by name.
// The handles are generic over a consumer-written scan function and take a
// sqlate.Session, so a handle runs against the pool or inside a transaction
// alike; every error is mapped through the dialect at the runner boundary.
// Statement text is build-time only: files under embed, never request
// input.
//
// The package exports:
//
// Catalog and compilation:
//
//   - [Catalog], the registered pattern sources every statement compiles
//     against, built by [NewCatalog] or [MustCatalog]
//   - [Source], one namespace's patterns, declared by [Publish], and
//     [Patterns], the library's own source under [Namespace]
//   - [Pattern], one catalog entry as the inventory reports it
//   - [Statements], the statements compiled from one directory, and
//     [Statement], one compiled file
//   - [Tier] with its constants [TierStandard] and [TierNative], a
//     statement's declared portability
//   - [Field], one entry of a projection base's field contract
//   - [Verifier], what can check itself against the live schema, and
//     [Verify], which runs several
//
// Arguments and scans:
//
//   - [Args], a statement's arguments by name, built by [With] and [ArgsOf]
//   - [Row], the current row a scan reads, and [ScanFunc], the scan a
//     consumer writes, with [Scanner] and [Scalar] as ready-made ones
//   - [Rows], the handle for a query that returns rows
//
// Collection reads:
//
//   - [Projection], the handle for a collection read over a projection base
//   - [Directives], one read request, with its [Sort] and [Filter]
//     declarations, [Op] and its constants [OpEq], [OpNe], [OpGt], [OpGe],
//     [OpLt], [OpLe], [OpLike], [OpIsNull], [OpIsNotNull], and [OpIn], and
//     [TotalMode] with its constants [TotalExact] and [TotalNone]
//   - [Page], an offset read's page declaration
//   - [Collection], one page of a read, with [NoTotal] for a total not
//     counted
//   - [Cursor], an opaque position a later page continues from
//   - [ErrDirectives], the request sentinel, and the request errors:
//     [UnknownFieldError] with its [FieldUse] and constants [FieldUseSort]
//     and [FieldUseFilter], [UnknownOperatorError], [InvalidValueError], and
//     [CursorError] with its [CursorReason] and constants
//     [CursorMalformed], [CursorMismatch], and [CursorUnsupported]
//
// Commands and guards:
//
//   - [Returning], the handle for a command that returns its changed row,
//     [Returner], the dialect capability that renders it as one statement,
//     and [Verb] with its constants [Insert] and [Update], the command kinds
//     it attaches to
//   - [Guard], the optimistic-concurrency protocol over a command and its
//     version check, and [RowGuard], its counterpart over a returning
//     command
//   - [ErrVersionMismatch], a guarded row at another version, and
//     [ErrRefused] with [RefusedError], a guarded row its command's own
//     predicate refused
//   - [ErrNotOneRow], a returning command that did not yield exactly one
//     row, and [ErrTransactionRequired], a statement that needs a
//     transaction run outside one
//   - [ArgumentError], a parameter the arguments did not bind
package query
