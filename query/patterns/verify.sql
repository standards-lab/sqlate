--| tier: standard
-- The field-contract probe: every declared field named over the base and
-- compared against a cast of its declared type, so a field the base no
-- longer outputs, or one whose type no longer matches, fails to prepare.
SELECT {{columns}} FROM ({{base}}) q{{where}}