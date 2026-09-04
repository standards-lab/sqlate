--| tier: standard
-- Offset paging in the standard form; an engine without it (MySQL, SQLite)
-- overrides this pattern by name from its own pattern source.
 OFFSET {{offset}} ROWS FETCH NEXT {{fetch}} ROWS ONLY