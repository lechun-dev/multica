# LC-797 project/task authorization rollout and recovery

This runbook is the release gate for the private project/task authorization
overlay. Code completion alone does not satisfy the production acceptance
items below; the release owner records each environment's evidence here or in
the linked change record.

## Phase contract

| Phase | New reader | Shadow comparison | Authorization changes |
| --- | --- | --- | --- |
| `off` | no | no | feature inactive |
| `shadow` | no; legacy result returned | yes | feature inactive |
| `reader` | yes | no | controlled by business permissions |
| `writer` | yes | no | controlled by business permissions |
| `restricted` | yes | no | controlled by business permissions |

Set `PROJECT_PERMISSION_ROLLOUT_PHASE` explicitly. The old
`PROJECT_PERMISSION_ENABLED=true` maps directly to `restricted` only for
backward compatibility and must not be used for a staged production rollout.
An invalid phase prevents server startup. The historical `reader`, `writer`
and `restricted` names remain for deployment compatibility; they do not grant
or revoke a user's ability to change authorization. Task/project management
permissions are the only write authority.

## Preflight

1. Back up PostgreSQL and record the restore point.
2. Apply additive migrations `509` through `526`; do not down-migrate after
   authorization writes have started.
3. Deploy a build that supports every phase while the phase remains `off`.
4. Run `make private-contracts`, the full server regression, frontend type
   checks/build, and `make upstream-check` from a clean branch.
5. Confirm the metrics listener is private and scraped. Authorization logs and
   metrics must never include subject IDs, resource IDs, task/comment bodies,
   email addresses, grant payloads, or ACL membership lists.

## Promotion gates

### `off` to `shadow`

Run shadow mode for the duration and representative traffic volume agreed by
the release owner. Record start/end time, request count, workspace mix and
release version. Promotion requires zero unexplained comparison differences
and zero shadow evaluation errors. Every mismatch must be classified and
linked to its resolution; do not average mismatches away.

### `shadow` to `reader`

Verify the permission dashboard against sampled known grants before enforcing
reads. Test inherited, restricted, projectless, direct-parent and expired-grant
cases. Also verify that authorized managers can update access and unauthorized
users remain read-only.

### `reader` to `writer`

After read enforcement is stable, verify direct user, organization, Everyone,
custom-role, mention, assignee and access-request changes become visible without
a stale authorization window. This phase change is operational only; mutation
eligibility continues to come from business permissions.

### `writer` to `restricted`

Use a disposable task first. Switch it to `restricted`, verify the project
source disappears while task Base and direct-parent Base remain, then revoke
and expire sources individually. Verify web, API, search, realtime subscription
and Agent claim behavior before broad use.

## Monitoring and alerts

Load `operations/lc797-projectauth-alerts.yml` into Prometheus-compatible rule
evaluation. The relevant metrics are:

- `multica_projectauth_decision_total{action,result}`
- `multica_projectauth_shadow_comparison_total{surface,result}`
- `multica_projectauth_operation_duration_seconds{surface}`
- `multica_projectauth_slow_operation_total{surface}`
- `multica_projectauth_agent_claim_total{action,result}`

The label values are bounded enums. Investigate every shadow mismatch/error,
Agent claim authorization error and sustained dashboard/export latency alert.
A denial-rate alert is an anomaly signal, not evidence that denials are wrong.

## Performance evidence

Before production promotion, run single resolve, batch resolve, list/search,
dashboard and export against production-shaped data. Record dataset sizes,
scanned rows, query plans, concurrency, P50/P95/P99 latency, error rate, CPU,
RSS and database load. The development microbenchmarks are useful regression
baselines but are not production acceptance evidence.

Development baseline on Apple M1 Max (three runs, in-memory fixtures): single
resolve ~4.82 µs/op, batch 100 ~128 µs/op, explain ~5.2 µs/op and preview
~8.1 µs/op. Re-run with:

```sh
cd server
go test -run '^$' -bench 'BenchmarkEffectiveAccess' -benchmem ./pkg/projectauth
```

## Emergency rollback

1. Set `PROJECT_PERMISSION_ROLLOUT_PHASE=reader` and restart the service.
2. Confirm the public config reports `reader`; restricted task reads remain
   protected and authorization changes still follow business permissions.
3. Do not set `off` and do not deploy a pre-reader binary while restricted
   policies or task grants exist.
4. Diagnose with bounded metrics and audit identifiers. Do not log or export
   task bodies or raw ACL payloads.
5. Restore `writer`/`restricted` only after the incident owner approves it.

Database migrations are additive. Application rollback is allowed only to a
build that understands the stored policy and maintains reader enforcement.
Database rollback requires a restore to the recorded pre-write backup and a
separate data-loss decision.

## Cache, subscription and recovery drills

Authorization decisions are not cached across requests. List SQL reuses
authorization state only within one statement. After granting, revoking,
expiring or changing policy, verify an already-open browser subscription and a
new request both converge immediately; reconnect once to cover replay behavior.

Run and record these drills before final acceptance:

- revoke one of several sources and confirm remaining sources still authorize;
- expire a grant and confirm reads, search and Agent claims fail closed;
- change organization ancestry and confirm the next request reflects it;
- switch Owner bypass at runtime and confirm both outcomes;
- inject storage failure, cross-workspace binding, unknown role/permission and
  mismatched resource binding, confirming fail-closed behavior;
- terminate/restart an app instance during ACL update and verify transaction,
  audit record and policy version remain atomic;
- exercise `restricted` → emergency `reader` → restored `restricted` and
  confirm business-authorized mutations behave consistently throughout.

## Acceptance record

For each phase, record release owner, date, version, environment, commands,
dashboards, traffic window, performance artifact, drill results and deviations.
The feature remains awaiting acceptance while any contract test, real-scale
performance result, shadow window, recovery drill, security review or release
owner sign-off is missing.
