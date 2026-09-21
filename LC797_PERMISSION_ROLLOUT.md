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

## Task permission levels

Task authorization is decided by the effective permission the caller holds on
the task being acted on. The levels are literal:

| Level | Grants |
| --- | --- |
| View | Read the task and its activity. |
| Edit | View, plus editing fields, commenting and creating child tasks. |
| Manage | Edit, plus granting access, revoking access, approving and rejecting access requests. |

`Edit` never implies `Manage`. A user who can edit a task cannot authorize
another person on it, nor decide that person's access request. Task-level roles
use their own matrix, independent of project roles, in
`server/pkg/projectauth/task_role_policy.go`.

Granting, revoking, previewing, listing and approving all evaluate the same
`IssueManage` check, so the surfaces cannot disagree. Whether a workspace Owner
is additionally exempt from that check is decided only by the switch below.

## Owner bypass

`PROJECT_OWNER_BYPASS_ENABLED` is the single deployment-level switch for the
workspace-owner override, parsed in `server/pkg/projectauth/service.go`. It is
read from the process environment, so changing it takes a restart; it is no
longer read from workspace settings.

- Unset, empty, or any value other than `false` (case-insensitive, surrounding
  whitespace ignored) keeps the historical permissive behavior: a workspace
  Owner passes task `Manage` checks without an explicit grant.
- `PROJECT_OWNER_BYPASS_ENABLED=false` removes that override. A workspace Owner
  then receives no automatic right to view, grant on, or approve access to a
  task, and must obtain access like any other member.

Set the switch explicitly in every environment instead of relying on the
permissive default, and confirm the intended value after any restart.

## Access requests with several approvers

A request is offered to every user who currently holds `Manage` on that task,
not only to the requester's manager or the workspace Owner. Each recipient gets
an inbox entry of type `task_access_request`, deduplicated on
`(workspace_id, request_id, recipient_user_id, event)` (migration `525`).

A request is decided exactly once. The first approval or rejection wins, and
every later attempt returns `409` with code `access_request_state_conflict` plus
the current status, so a stale decision cannot overwrite a settled one.
Approvers who did not act see the settled outcome instead of action buttons.
Opening the inbox entry navigates to the task with the authorization dialog
focused on that request.

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
6. Set `PROJECT_OWNER_BYPASS_ENABLED` explicitly for the environment and record
   the intended value. An unset variable keeps the permissive owner override.

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
- restart with `PROJECT_OWNER_BYPASS_ENABLED` set to the opposite value and
  confirm both outcomes, then restart again and confirm the setting is read
  from the environment rather than from stored workspace state;
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

## Verification state at 2026-09-20

Measured on a freshly created database with migrations `509` through `527`
applied plus the optional DingTalk extension schema. That extension schema is
not optional for the backend suite: handler tests assume its tables exist, and
`scripts/dingtalk-notify-migrate.sh --apply` is the supported way to create
them. Set `DATABASE_URL` explicitly when running those tests, because the test
main exits successfully without it and the run then proves nothing.

Passing on that database:

- `go build ./...`, `go vet ./internal/handler/ ./pkg/projectauth/
  ./internal/service/` and `gofmt` are clean.
- `go test ./internal/handler/... ./pkg/projectauth/...` is **green: zero
  failing tests**, down from the 40 this branch carried before this pass.
- The task-authorization contracts pass, including `TestIssueAccessControl*`,
  `TestApplyIssueAccessControl*` and `TestIssueAccessRequest*`.
- The concurrency contracts pass repeatedly, not just once:
  `FailTaskAndRerunConcurrently*` and `TaskWriteFence*` were run three times
  over (81 s) with no failure.
- Frontend `pnpm typecheck` and the full `packages/views` Vitest suite pass
  (441 files, 5330 tests).

The 40 failures were not caused by the task-authorization change itself. Most
were collateral from an earlier commit on this branch that enforced agent-use
authorization without reconciling the paths the attribution model already
documents as carrying no authorizing human; the rest were contracts that had
gone stale against the code they guard, or gaps the enforcement exposed. What
this pass changed:

- Agent use is judged by the human the run acts for — its originator when one
  exists, otherwise its accountable human (`AuthorizationSubject`) — instead of
  refusing every run the attribution model documents as carrying none.
- The Autopilot read gate judges the human an agent-originated request acts for
  in addition to the authenticated member, so a mediated request is not hidden
  from the member who ordered the autopilot.
- `CreateAutopilotTrigger` stamps the immutable `created_by` principal that
  migration 490 documents as written-once-at-creation.
- Webhook admission resolves the principal a run fires as from the trigger's
  `created_by_id` and fails closed when there is none, which migration 490
  documented but no code implemented — the column was never read.
- Deleting a task now removes its source-context row and snapshot clones in the
  same transaction and leaves one durable deletion intent per clone, wiring up
  `DeleteIssueSourceContextByIssue` and
  `RecordSourceContextDeletionObjectIntent`, which had no caller.
- `FailTask` retries its transaction on a concurrency conflict
  (`40P01`/`40001`) instead of surfacing the deadlock and stranding the parent
  in `running`. `RerunIssue` already reclaimed and retried under this race;
  failing a task now tolerates the same contention.
- Migration `527` drops `agent_daily_stats.workspace_id`'s foreign key. That
  constraint made every task status write take `FOR KEY SHARE` on the workspace
  row through the stats trigger, so a status-only update stalled behind the
  workspace-delete fence's `FOR UPDATE` — contradicting the fence's own design,
  in which `lock_task_owner_rows` governs ownership writes only. Cleanup is
  unaffected: teardown deletes the workspace's agents and the remaining
  `agent_id` cascade takes the stats rows with them.
- The table-rows property filter accepts operator members, which it previously
  could not even express.
- A list-count failure now answers `500 failed to count issues` instead of
  degrading to a page-sized total behind a `200`.
- Stale contracts were aligned with the code they guard: the search parity
  scanner's column count, the visibility predicate markers, the grant-predicate
  assertions, the count-failure injection matcher, the owner-scope list fixture,
  the squad-briefing heading, the comment-fold fixture's thread, and one test
  that replaced its own route context before reading a URL param.

One display gap surfaced in the test environment and is fixed. The share dialog
manages the task's *manual* ACL, and the read endpoint deliberately returned only
`source='manual'` rows. Mentioning somebody stores a real grant with
`source='system'`, so a task whose only other reader arrived through a mention
reported "already granted 0" — true of the manual ACL, but it reads as "nobody
has access". The read now also returns those grants as `derived_grants`, each
labelled with why it exists (`creator`, `assignee`, `mention`, or the stored
source), and the dialog lists them read-only beside the manual rows, counted
together with them. They are deliberately not editable there: the source that
granted them is the only thing that can withdraw them, so the manual-ACL API
stays the single write path.

That first cut of the derived list then broke the share dialog in the test
environment, and the shape of that failure is worth remembering. Migration `469`
backfills the legacy `issue_permissions` rows as grants that name a permission
and leave `role_key` NULL — the grants table's CHECK allows exactly one of the
two. The derived query was the first read of that table *without* the
`source='manual'` filter, so it met those NULLs and failed the scan, turning the
whole read into `500 project_permission_failed`. A clean test database never has
such rows, so the suite stayed green while a workspace with real history could not
open the dialog at all. Both halves are fixed: the derived query keeps only
role-bearing rows and the role is read as nullable, so one odd grant can never
fail the endpoint, and only a 403 takes the permission branch in the dialog.
When touching this table, remember that `role_key` and `permission` are mutually
exclusive and that the backfilled rows are permission-shaped.

Withdrawing mention access now has a supported path, and it is worth knowing how
it works before debugging one. A mention is stored as a grant, and every comment
or description change reconciles that storage against the text; deleting the
comment used to be the only way to withdraw it, and deleting one never
reconciled at all (fixed here). The access list can now withdraw it directly:
the grant is deleted and a watermark is recorded per task and person
(`projectauth_issue_mention_revocations`), which reconciliation compares a
mention against. A mention older than the watermark stays withdrawn; mentioning
the person again outranks it and grants again; a mention in the description has
no timestamp of its own, so the digest of that text at withdrawal time decides.
The list also writes immediately now — removing a row persists on the spot
instead of waiting for a save in the other dialog.

Two things to keep in mind when re-running this suite. Run it against one
database at a time, and prefer a freshly created database: a reused one
accumulates rows from earlier failed runs, fixed member emails then collide, and
four collaborator tests fail on a `user_email_key` duplicate that has nothing to
do with the code under test. And `make sqlc` currently fails on this branch —
sqlc reports `column reference "workspace_id" is ambiguous` for
`workspace_delete.sql` while PostgreSQL executes the same statement without
complaint — so regenerating requires working around that or hand-editing the
generated file, as was done for `CreateAutopilotTrigger`.
