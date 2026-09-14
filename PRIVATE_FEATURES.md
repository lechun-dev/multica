# MissionOS Private Feature Contracts

This file is the source of truth for behavior that the private MissionOS fork
must preserve when synchronizing with upstream Multica.

## Maintenance rule

Every private feature or behavior change must update this file in the same
change. Update the protected-behavior table, its contract coverage, or the
private change register as appropriate. A feature is not complete when its
code changes but this inventory does not.

CI enforces this rule for product-code changes with
`scripts/check-private-feature-inventory.sh`. This includes upstream merges:
the inventory must be reviewed and updated when official changes affect the
private integration branch.

## Branch and remote model

- `main` tracks `private/main` and is the deployable private integration branch.
- `origin/main` is the official Multica upstream used for synchronization.
- Upstream changes are first checked with `make upstream-check`; the command
  performs its merge simulation in a temporary worktree and never modifies the
  current checkout.
- A real upstream merge is complete only after the contracts below pass and the
  resulting diff has been reviewed for accidental removal of private files.

## Protected behavior

| Area | Required private behavior | Primary boundary | Contract coverage |
| --- | --- | --- | --- |
| Task authorization | A task may be projectless. Task creator, assignee, originator, and mentioned people receive task-level access. Mentioning a person is equivalent to granting task `MEMBER` access; it does not grant project membership. Task roles use a task-only matrix and persistence catalog; same-named project roles never define task permissions. Project permissions reach a task only through the central explicit projection. Effective access merges current-task Base, the current project's projected permissions only in `inherit` mode, and only the direct parent's Base; it reports every contributing source and fails closed on invalid storage data. Task `MEMBER` includes View, Edit, Comment, and ChildCreate only. Manual task policy and ACL replacement is atomic, optimistic-versioned, expirable and audited. Access requests, dashboards, exports, Agent claims, cross-project direct children and all read/write paths reuse the resolver. Rollout is staged (`off` → `shadow` → `reader` → `writer` → `restricted`); emergency rollback stops writes at `reader` without exposing restricted tasks. | `server/pkg/projectauth`, `server/internal/handler/projectauth_*`, `server/internal/handler/issue_access_control.go`, additive UI components, `server/migrations/509_*` through `526_*` | `TestEffectiveAccess*`, `TestApplyIssueAccessControl*`, `TestIssueAccessControl*`, `TestAccessGrantRoleScopeIsExplicit`, `TestProjectPermissionProjectionIsExplicitAndTaskCapped`, `TestDefaultTaskPolicyMatchesDocumentedSystemRoles`, `TestTaskSystemRoleDefinitionsAreScopedAndIndependent`, `TestTaskRolePermissionsUseIndependentPersistentMatrix`, `TestLC797PermissionModelMigrations*`, rollout/mutation-boundary, list/read, access-request, dashboard/export, Agent-claim and cross-project-parent contracts |
| Authorized list and progress reads | Reuse the existing ACL rules within each SQL statement, never across requests. Preserve owner-bypass configuration, task-only grants, active organization ancestry, empty role overrides, exact pagination totals, archived-child handling, and canonical/custom terminal status precedence. Unchanged realtime snapshots do not refetch child progress; status, hierarchy, and visibility changes still do. | `server/internal/handler/issue_visibility_sql.go`, `packages/core/issues/ws-updaters.ts` | `TestIssueVisibilityCTEsMatchStandalonePolicy`, `TestTerminalIssueStatusSetMatchesEffectiveStatus`, `ws-updaters.test.ts` |
| Inbox and historical notifications | A directly mentioned user can see the inbox entry and open the linked task, including projectless tasks and historical records whose access was materialized. | `server/internal/handler/inbox.go`, project authorization adapters | `TestListInboxShowsDirectMentionOutsideProjectMembership`, `TestInboxListsShipCommentPreviewNotFullComment` |
| Agent chat replies | Persisted agent replies remain visible when realtime events contain only a projection, and queued turns stay paired with their own replies. | `server/internal/handler/chat.go`, `packages/core/chat`, `packages/views/chat` | `TestCompleteTask_ChatNonEmptyOutputWritesMessage`, `TestCompleteTask_ChatCallbackIdempotent`, `TestDirectChat_ClaimKeepsQueuedTurnsPairedWithReplies`, selected chat UI tests |
| DingTalk personal delivery | Human mentions and agent completion notifications are delivered through the private DingTalk integration without exposing internal mention links or routing IDs. | `extensions/dingtalk-notify`, `server/internal/integrations/dingtalkpersonal` | DingTalk extension tests and `TestDingTalkPersonalMessage*` |
| Desktop branding and release channels | Production builds use MissionOS identity; preview builds use MissionOS Preview identity and beta badge. Update channels and changelog links remain private. | `apps/desktop/src/shared/desktop-identity.ts`, private packaging scripts and workflows | desktop identity, updater, and packaging tests |
| Private release notes | Private production and preview releases publish the MissionOS changelog rather than the official Multica changelog. | `apps/web`, desktop changelog link, private release workflows | release changelog policy tests |
| Task CLI discovery | Desktop ships `missionos`; task PATH exposes both `multica` and `missionos` as the same binary. If the command is missing, agents must stop and report it instead of searching the disk. | `server/internal/daemon/task_cli_bin.go`, `apps/desktop/scripts/bundle-cli.mjs`, runtime brief and platform skill | `TestEnsureTaskCLIBinDir_*`, `TestInjectTaskCLIPATH_*`, `TestWriteAlwaysUseCLIForbidsDiskSearch` |

## Private change register

| Date | Change | Inventory impact |
| --- | --- | --- |
| 2026-09-14 | Completed LC-797 staged rollout controls and bounded authorization telemetry. Added `off`, `shadow`, `reader`, `writer`, and `restricted` phases, a centralized service mutation boundary, UI write gating, shadow comparison, denial/latency/Agent-claim metrics, alerts and an operator recovery runbook. | Emergency rollback returns to `reader`, never the legacy reader-off path; all ACL mutations stop while existing restricted data remains protected. Metric labels contain bounded operation/result values only, not subject IDs, resource IDs, task content, or ACL bodies. |
| 2026-09-14 | Enforced LC-797 authorization for Agent task claims and continuations, including explicit denial reasons and race-safe claim checks. | Keeps agent authorization in the central resolver and records only bounded claim outcomes; no Agent-specific permission matrix exists. |
| 2026-09-14 | Added effective-access dashboards and exports by person and by project/task, including role scope, source resource, expiry and policy version. | Read-only reporting reuses resolver explanations rather than maintaining a second authorization algorithm; CSV export redacts task content and grant payloads. |
| 2026-09-14 | Added task ACL management UI, independent task-role administration and permission-source explanations. | Additive private components consume typed APIs; rollout phase makes them read-only until writer activation, limiting conflict with upstream screens. |
| 2026-09-14 | Applied effective task authorization to single/list/search/realtime/read and mutation paths with fail-closed storage and binding checks. | Shared upstream handlers retain thin calls into private adapters; single, batch, explain, preview and read paths share the core resolver. |
| 2026-09-14 | Added direct-parent task linkage across the same project, another project or no project, with cycle/workspace validation. | Only the direct parent's Base is inherited; effective permissions are never recursively resolved, so grandparent access cannot cross the boundary. |
| 2026-09-14 | Added LC-797 task-role access requests with requester-safe responses, effective Owner-only review, transactional grant/audit updates, expiry and cancellation state transitions, retry-safe inbox delivery, and best-effort asynchronous DingTalk enqueue. | Keeps the private approval workflow behind additive task endpoints and the central effective-access resolver; notification delivery has its own private ledger and does not alter upstream inbox contracts. |
| 2026-09-14 | Added LC-797 atomic task policy and manual-grant replacement APIs with optimistic version conflicts, dry-run impact preview, expiry constraints, authorization boundaries and one audit event per effective change. | Keeps task ACL writes in a private adapter and makes retries no-op when the desired state is already current; legacy grant payloads now carry optional expiry metadata. |
| 2026-09-14 | Added LC-797's scope-safe `EffectiveAccessResolver` for single, batch, explanation, and policy preview decisions, with explicit project-to-task projection and direct-parent Base inheritance. | Protects the central authorization algorithm and explanation metadata as a low-coupling private module; all read-path adoption, UI, dashboard, Agent enforcement, performance, and rollout remain separately gated. |
| 2026-09-14 | Began LC-797 permission model foundations: independent task role policy/persistence, task ChildCreate permission, issue policy, grant constraint, and access-request schema with concurrent indexes. | Extends the task authorization contract without changing upstream issue tables or adding database foreign keys. Remaining LC-797 resolver, APIs, UI, dashboard, Agent, and rollout units stay gated by their own acceptance standards. |
| 2026-09-14 | Reduced redundant child-progress refreshes and reused statement-local authorization sets for expensive issue reads. Replaced repeated terminal-status function calls in progress SQL with equivalent status sets. | No permission policy, exact-total API, or schema changes. Cross-request TTL caching is deferred until revocation-safe invalidation exists. |
| 2026-09-12 | Required every private product-code change to update this inventory, enforced by the pull-request checklist and CI. | Prevents new private behavior from being omitted from future upstream-sync reviews. |
| 2026-09-12 | Added private feature contracts, migration collision protection, and upstream merge simulation. Moved comment authorization calls behind a private adapter. | Established the protected-behavior table and synchronization workflow; no task authorization behavior changed. |
| 2026-09-12 | Desktop CLI alias: task PATH and bundled bin expose both `multica` and `missionos`; briefs forbid disk search. | Stops agents from spending the first minutes of a task searching for `multica`. |

Run the focused suite with:

```sh
make private-contracts
```

The handler contracts require the normal local PostgreSQL test database. The
target prepares it with the repository's existing migration workflow.

## Extension boundary rule

Issue-read optimization measurements and rollout checks: [PERFORMANCE.md](PERFORMANCE.md).

Private behavior should live in private modules or adapter files. Shared
upstream handlers may call a small private hook, but should not contain the
authorization, delivery, or branding policy itself.

Current preferred boundaries:

- Authorization policy: `server/pkg/projectauth`
- HTTP and persistence adapters: `server/internal/handler/projectauth_*` and
  `server/internal/handler/private_*_extension.go`
- DingTalk delivery: `extensions/dingtalk-notify`
- Desktop identity: `apps/desktop/src/shared/desktop-identity.ts`

When a new private change must touch an upstream-owned file, add or extend a
contract test first, keep the upstream edit to a thin call site, and record the
new behavior in this table.

## Upstream synchronization workflow

1. Start from a clean `main` that tracks `private/main`.
2. Run `make upstream-check`. It fetches both remotes, checks migration-number
   conflicts, simulates the merge, and runs the protected contract suite.
3. Review the simulated conflict report. Resolve real conflicts in favor of
   upstream structure while preserving the contracts above.
4. Perform the real merge without rebasing or rewriting published private
   history.
5. Run `make private-contracts` and the normal `make check` before release.
