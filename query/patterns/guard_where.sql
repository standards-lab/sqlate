--| tier: standard
-- The optimistic-concurrency guard's predicate, over a guarded table's id
-- and version columns; a command includes it as {{> sql.guard_where}}.
id = {{id}} AND version = {{version}}