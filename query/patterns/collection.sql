--| tier: standard
-- The collection read: any authored base as a derived table, the request-
-- shaped clauses attached outside it. q is the derived table's correlation
-- name, which standard SQL requires; every predicate and sort term says
-- q.<field> so it names the base's output column, never a same-named
-- column inside the base. Every slot is text the library composed from
-- other patterns; request values never enter as text.
SELECT * FROM ({{base}}) q{{where}}{{order}}{{paging}}