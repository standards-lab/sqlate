# Dollar-quoted strings in the `sqlint` linter

The `sqlint` package strips single-quoted literals, double-quoted identifiers, and comments before
it matches a native form or the reserved delimiter (`sqlint/body.go`, `codeOnly`). It does not
strip PostgreSQL's dollar-quoted strings (`$$ … $$`, `$tag$ … $tag$`), so it lints text inside one
as code.

Planned: `codeOnly` strips dollar-quoted bodies, tracking the open tag across lines the way it
tracks a block comment. The trigger is the first false positive, most likely from a function body
in a migration. The plan assumes the native-form check stays line-based.
