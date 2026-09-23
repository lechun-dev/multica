-- 2026-09-23 coder(lq): The column belongs to migration 537, so rolling back
-- this repair must not remove it from installations with a healthy schema.
SELECT 1;
