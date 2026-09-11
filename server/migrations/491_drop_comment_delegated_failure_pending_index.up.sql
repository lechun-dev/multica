-- 2026-09-12 coder(lq): Upstream migration 450 is renumbered to 491 to preserve the released private migration history.
-- Migration 445 replaced this index with the smaller unsettled-only partial
-- index after the historical recovery backfill completed.
DROP INDEX CONCURRENTLY IF EXISTS idx_comment_delegated_failure_pending;
