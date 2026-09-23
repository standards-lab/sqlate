# Dollar quoting in the lint

`sqlint` strips single-quoted literals, double-quoted identifiers, and comments before it
matches a native form or the reserved delimiter (`sqlint/body.go`, `codeOnly`). It does not strip
PostgreSQL's dollar-quoted strings (`$$ … $$`, `$tag$ … $tag$`), so text inside one is linted as
code.

Planned: strip dollar-quoted bodies in `codeOnly`, tracking the open tag across lines the way it
tracks a block comment. The trigger is the first false positive, most likely a function body in
a migration.

Assumes the native-form check stays line-based.
