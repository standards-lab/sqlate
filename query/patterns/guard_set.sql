--| tier: standard
-- The protocol columns a guarded command advances; the command's own SET
-- list precedes it.
updated_at = CURRENT_TIMESTAMP, version = version + 1