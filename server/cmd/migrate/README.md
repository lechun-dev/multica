# Migration runner operations

## Prebuild the cancelled-chat session guard index

Migration 506 builds `idx_agent_task_queue_chat_with_session_created_at`, which
keeps `AdvanceCancelledChatSessionPointer` from scanning the global task queue
while a cancel or late session pin holds the chat row lock. This is a net-new
index on `agent_task_queue`, and most historical chat tasks may satisfy its
predicate. Measure the eligible population before scheduling the build:

```sql
SELECT count(*) AS indexed_rows
FROM agent_task_queue
WHERE chat_session_id IS NOT NULL
  AND session_id IS NOT NULL;
```

Migrations run during backend startup, whose Helm startup probe allows ten
minutes. On a large production table, check for long-running transactions and
prebuild the index in a low-traffic window. Run the statement by itself and
outside a transaction:

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_task_queue_chat_with_session_created_at
ON agent_task_queue (chat_session_id, created_at DESC)
WHERE chat_session_id IS NOT NULL
  AND session_id IS NOT NULL;
```

Confirm the build is usable before deploying:

```sql
SELECT indexrelid::regclass AS index_name,
       pg_size_pretty(pg_relation_size(indexrelid)) AS size,
       indisvalid,
       indisready,
       indislive
FROM pg_index
WHERE indexrelid = to_regclass('idx_agent_task_queue_chat_with_session_created_at');
```

The migration then becomes a fast no-op. If an interrupted manual build leaves
the index invalid, drop that invalid index concurrently and retry the standalone
build before deploying. The migration runner also registers invalid-index
cleanup so an interrupted startup build can recover on its next attempt.

Application rollback is compatible with the extra index, so leave it in place.
Rolling back migration 506 drops the index and is functionally safe, but restores
the global scan and longer chat-lock hold time.

## Issue description search index retirement

Migrations 504 and 505 retire the two historical issue-description search
indexes: `idx_issue_description_bigm` and `idx_issue_description_trgm`.
`SearchIssues` now scans the selected workspace's issue candidates and evaluates
description matches during projection, so neither global description GIN is
read. Fresh installs also skip migration 139's historical trigram build. Do not
repair or recreate these indexes after applying migrations 504 and 505.

The down migrations restore the trigram index everywhere and also restore the
CJK-friendly bigram index where `gin_bigm_ops` is available. Both use
`CREATE INDEX CONCURRENTLY` and retry safely after an interrupted build.

These migrations run during backend startup. A concurrent drop can wait for old
transactions, so for multi-gigabyte production indexes prefer a low-traffic
window: check for long-running transactions, then run each statement separately
and outside a transaction before deploying:

```sql
DROP INDEX CONCURRENTLY IF EXISTS idx_issue_description_bigm;
DROP INDEX CONCURRENTLY IF EXISTS idx_issue_description_trgm;
```

The subsequent migrations become fast no-ops. If startup performs a drop and
is interrupted, `IF EXISTS` makes the next run retry safely; one index may
remain until that retry completes. Rolling back to the current candidate-first
search remains functionally correct without either index, but rolling back to
the legacy search can make description queries much slower or time out until
the database rollback rebuilds the indexes. Rebuilding up to two multi-gigabyte
GIN indexes is not immediate; verify every restored index is live, ready, and
valid before relying on it:

```sql
SELECT indexrelid::regclass AS index_name, indisvalid, indisready, indislive
FROM pg_index
WHERE indexrelid IN (
    to_regclass('idx_issue_description_bigm'),
    to_regclass('idx_issue_description_trgm')
);
```

## Comment content search index retirement

Migrations 495 and 496 retire both historical comment-content search indexes:
`idx_comment_content_bigm` on pg_bigm deployments and the portable
`idx_comment_content_trgm` fallback. `SearchIssues` now scans comments through
`idx_comment_workspace` and evaluates content matches during aggregation, so it
no longer reads either global content GIN. Fresh installs also skip migration
140's historical fallback build. Do not repair or recreate these indexes after
applying migrations 495 and 496.

The down migrations restore exactly one historical index with
`CREATE INDEX CONCURRENTLY`: migration 495 restores the pg_bigm index where
`gin_bigm_ops` is available, while migration 496 restores the pg_trgm fallback
everywhere else.
Both restore the original `LOWER(content)` expression and retry safely after an
interrupted concurrent build.

These migrations run during backend startup. A concurrent drop waits for old
transactions, and the Helm startup probe allows ten minutes before restarting
the pod. For the multi-gigabyte production index, prefer a low-traffic window:
check for long-running transactions, then run each statement separately and
outside a transaction before deploying:

```sql
DROP INDEX CONCURRENTLY IF EXISTS idx_comment_content_bigm;
DROP INDEX CONCURRENTLY IF EXISTS idx_comment_content_trgm;
```

Verify that `idx_comment_content_trgm` reports all three flags as `true` before
resuming traffic. If `idx_comment_content_bigm` is repaired later, keep the
fallback until the bigram index also reports all three flags as `true` **and**
has the exact migration 036 shape: a non-unique, non-partial GIN index on
`LOWER(content)` using the `pg_bigm`-owned `gin_bigm_ops` operator class. Only
then can the fallback be dropped with `DROP INDEX CONCURRENTLY` during a
maintenance window.
