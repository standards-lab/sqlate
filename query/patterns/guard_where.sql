--| tier: standard
-- The optimistic-concurrency guard's predicate, over the architecture's
-- identity columns; a command includes it as {{> sql.guard_where}}.
id = {{id}} AND version = {{version}}