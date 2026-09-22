--| tier: standard
--| alternate: columns, op, values
-- The keyset predicate: the rows past a cursor's row under the keyed
-- ordering, composed from the filter patterns as the expanded chain of
-- disjuncts over the keyed terms. An engine with row-value comparison
-- respells it over the alternate slots as ({{columns}}) {{op}} ({{values}}),
-- where op is > or <, and every value is bound once in either spelling.
({{disjuncts}})