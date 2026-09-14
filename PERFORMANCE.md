# Issue Read Performance

## 2026-09-14: Permission-heavy list and child-progress reads

Production diagnosis supplied for this change: about 695 issues, 1308 grants,
concurrent expensive exact counts and child-progress aggregates, high PostgreSQL
CPU without lock or disk waits. These observations motivate this work; they are
not measurements of the patched release.

### Changes and boundaries

- Ignore unchanged status/parent fields in WebSocket full snapshots when deciding
  whether to refresh child progress. Real status, hierarchy, visibility-related
  changes and authoritative change flags still invalidate it; a missing baseline
  is handled conservatively. Other list invalidations are unchanged.
- Reuse the existing permission predicates through statement-local materialized
  organization, project and issue sets. List rows and exact counts use the same
  rule; child progress checks both parent and child against one visible set.
- Resolve terminal custom statuses as a set instead of calling
  `issue_effective_status` per child. Canonical keys still override the catalog.
- No schema, role, owner-bypass configuration, archive counting, pagination,
  total-response or authorization-policy changes. No production changes made.
- No cross-request caching or coalescing was added. TTL-only invalidation could
  expose outdated counts after revocation. Permission-aware invalidation must be
  established before pursuing that separate change.
- Do not add a 30-second frontend `staleTime`: the existing shared default is
  `Infinity`. The demonstrated duplicate refresh originates in invalidation.

### Measurement

Disposable PostgreSQL 17.11, Apple M1 Max, local TCP, synthetic 701 issues and
1300 organization grants. Compare old standalone predicates with new SQL on the
same fixture. Ten warmup executions precede each 100-iteration sample, five
samples per case, crossing prepared-plan warmup for both variants. Progress
consumes the actual total and done aggregates, so the optimizer cannot discard
the work being measured. No production database or concurrency load was used.

Observed milliseconds per operation, min-max across five samples:

| Fixture | Query | Before | After |
| --- | --- | --- | --- |
| 1 project | Exact count | 0.796-0.804 | 0.686-0.708 |
| 1 project | Child progress | 2.845-2.903 | 0.926-0.965 |
| 1 project | Count with exact issue ID | 0.664-0.676 | 0.428-0.453 |
| 10 projects | Exact count | 0.830-0.852 | 0.724-0.758 |
| 10 projects | Child progress | 2.963-3.091 | 0.977-0.996 |
| 10 projects | Count with exact issue ID | 0.698-0.771 | 0.467-0.490 |

The early three-iteration measurements mixed prepared-plan modes and had large
variance; they were discarded in favor of the warmed measurements above.
The synthetic fixture does not reproduce production's minute-long waits or
distribution of all permission types. These are SQL microbenchmarks, not HTTP
latency or production CPU guarantees. Larger workspaces and different selectivity
remain rollout checks because materializing all visible IDs has an up-front cost.

To reproduce, migrate a **disposable** database and run from `server`:

```sh
DATABASE_URL='<disposable PostgreSQL URL>' go test ./internal/handler \
  -run '^$' -bench '^BenchmarkIssueVisibilitySQL$' -benchtime=100x -count=5
```

### Functional verification

New database tests compare visible IDs and progress with standalone ACL results
through owner-bypass toggles, include-workspace-owned modes, project/task creator
fallbacks, projectless assignees/shares, direct grants, task-scoped roles,
cross-project hierarchy, active organization ancestry, cycles, disabled
organizations, explicit empty roles, revoked grants and removed membership.
HTTP pagination retains exact totals, including beyond the last page. Custom
terminal status tests include canonical-key overrides and unknown keys.

Focused frontend tests: 122 passed; core typecheck and changed-file lint passed.
The updated `scripts/test-private-contracts.sh` passed end to end, including the
new database checks, authorization policy, DingTalk extension, chat rendering,
child-progress refresh and desktop identity/update contracts.
Broader frontend suite: 14 existing failures in API task-history parsing and
inbox tests, all reproduced in a separate HEAD export. Broader backend selection:
297 passing test/subtest events; every failing test also failed in the HEAD
export. Existing failures concern autopilot authorization fixtures, count-error
response expectations, a SQL-string assertion, owner visibility expectations and
property-filter decoding. These failures were not hidden or changed in this work.

Before deployment, review those baseline failures separately. After an approved
rollout, compare SQL call rate, mean/p95 duration, active queries, PostgreSQL CPU
and Agent dispatch latency over equivalent traffic windows. Verify revocation,
owner configuration, child status transitions and parent moves in the deployed
environment. Do not attribute Agent queue time solely to SQL without tracing it.
