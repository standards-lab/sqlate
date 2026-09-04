// Package query runs authored SQL files: Statements compiled from an fs.FS
// against a Catalog give a domain its statements by name, each file's
// "--|" header declaring its tier, the engine feature a native file uses,
// whether it needs a transaction, and, for a projection base, its key and
// field contract. The catalog is the registered pattern sources (the
// library's own Patterns(), an application's, an engine's overlay), built
// once at the composition root; a statement includes a pattern with
// {{> ns.name}}. Parameters, written
// {{name}} or {{name:type}} (the type binding through CAST, verbatim),
// resolve to the dialect's positions once at load; arguments bind from Args
// by name. The engine receives the body: the header is the loader's. The
// runners are generic over a consumer-written scan function and
// take a Session, so a handle runs against the pool or inside a
// transaction alike; every error is mapped through the dialect at the
// runner boundary. Verify prepares every statement against the live schema.
// Statement text is build-time only: files under embed, never request
// input.
package query
