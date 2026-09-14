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
| Task authorization | A task may be projectless. Task creator, assignee, and mentioned people receive task-level access. Mentioning a person is equivalent to granting task `MEMBER` access; it does not grant project membership. | `server/pkg/projectauth`, `server/internal/handler/projectauth_*` | `TestCreateIssuePromotesAssigneeAndMentionedMember`, `TestCreateCommentPromotesMentionedMember*`, `TestUpdateCommentPromotesMentionedMember*`, `TestProjectlessIssueCreateAndTriggerPreviewAllowedWhenPermissionsEnabled` |
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
