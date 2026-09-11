-- 2026-09-12 coder(lq): Upstream migration 446 is renumbered to 487 to preserve the released private migration history.
DROP INDEX CONCURRENTLY IF EXISTS idx_issue_properties_bigm;
