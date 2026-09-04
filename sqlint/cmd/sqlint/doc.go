// Command sqlint runs the sqlint package over a module: it reads
// sqlint.toml at the root given as its one argument (the working directory
// by default), resolves module paths through go list in that module, walks
// the tree, and prints every finding as file:line: message, sorted. It
// exits 1 on any finding or configuration error and prints "sqlint: ok"
// otherwise.
package main
