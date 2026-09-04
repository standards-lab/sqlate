--| tier: standard
-- The collection read's count twin, under the same filters.
SELECT COUNT(*) FROM ({{base}}) q{{where}}