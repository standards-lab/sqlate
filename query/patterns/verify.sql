--| tier: standard
-- The field-contract probe: every declared field named over the base, so
-- a field the base no longer outputs fails to prepare.
SELECT {{columns}} FROM ({{base}}) q