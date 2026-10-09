# Official upstream synchronization — 2026-10-10

Worktree: `/Users/lq/work/lechun/code/multica-upstream-20261010`

Branch: `codex/upstream-sync-20261010`

Private base: `e32d5b6a0`, including latest private main `1550bd456`.
Official upstream: `10a7e519d` (130 incoming commits). Latest remote references were fetched again on 2026-10-10.

## Integration decisions

Preserve MissionOS desktop identity, private update/release channels, bundled Codex CLI and legacy fallback, project/task authorization, organization and response caches, owner runtime-model configuration, source restrictions, mention grants, and prose-only inline execution summaries. Adopt official settings groups and shared Preferences for task/chat settings, Channels/Apps and wakeups.

Duplicate relationship hydration uses the current human principal and live task visibility; marking duplicates requires visibility of the target. Supplement/retry and steering enforce Comment and AgentUse. PR completion and supplement SQL retain archive predicates. Private task updates use upstream duplicate locks and wakeup cleanup inside the authorization transaction. Comment creation, attachment binding, and mention grants commit together. Agent history applies live authorization before pagination. System wakeup configuration requires task Manage; dispatch checks live AgentUse before enqueue, archived parents stay inactive, and archived children are excluded from the completion barrier. Search/grouped/unread optimizations from `1550bd456` remain present.

Upstream settled claim failures return `200` with `task:null`; invalid foreign-workspace task identity remains persisted as failed. French is new upstream: missing private strings reuse existing French where possible, with explicit English fallback for remaining private labels. It is not a complete French translation.

## Migration compatibility

All 1,260 published private migration files retain their exact filenames and bytes. Incoming official migrations 535–572 use unused versions 602–639; matching up/down files and hooks are adjusted together. Fresh PostgreSQL 17 migration and upgrade from the published private migration set both succeeded. Published identities must never be renumbered during deployment.

| Official migration | Merged migration |
| --- | --- |
| `535_github_pr_address_index` | `602_github_pr_address_index` |
| `536_issue_duplicate_of` | `603_issue_duplicate_of` |
| `537_issue_duplicate_of_index` | `604_issue_duplicate_of_index` |
| `538_task_supplement` | `605_task_supplement` |
| `539_task_supplement_request_index` | `606_task_supplement_request_index` |
| `540_task_supplement_capability_index` | `607_task_supplement_capability_index` |
| `541_task_supplement_comment_index` | `608_task_supplement_comment_index` |
| `542_task_supplement_primary_key` | `609_task_supplement_primary_key` |
| `543_task_supplement_teardown_guard` | `610_task_supplement_teardown_guard` |
| `544_task_supplement_application_settlement` | `611_task_supplement_application_settlement` |
| `545_pr_auto_complete` | `612_pr_auto_complete` |
| `546_issue_pr_automation_workspace_index` | `613_issue_pr_automation_workspace_index` |
| `547_issue_pull_request_exclusion_workspace_index` | `614_issue_pull_request_exclusion_workspace_index` |
| `548_task_supplement_comment_task_index` | `615_task_supplement_comment_task_index` |
| `549_task_supplement_comment_task_primary_key` | `616_task_supplement_comment_task_primary_key` |
| `550_comment_suppressed_agents` | `617_comment_suppressed_agents` |
| `551_pr_merge_status` | `618_pr_merge_status` |
| `552_agent_task_history_page_index` | `619_agent_task_history_page_index` |
| `553_wakeup_expiry` | `620_wakeup_expiry` |
| `554_wakeup_expiry_index` | `621_wakeup_expiry_index` |
| `555_wakeup_system_rule` | `622_wakeup_system_rule` |
| `556_wakeup_system_rule_index` | `623_wakeup_system_rule_index` |
| `557_wakeup_conditions` | `624_wakeup_conditions` |
| `558_issue_child_event` | `625_issue_child_event` |
| `559_issue_child_event_id` | `626_issue_child_event_id` |
| `560_issue_child_event_pending` | `627_issue_child_event_pending` |
| `561_search_index_change` | `628_search_index_change` |
| `562_search_index_change_workspace_index` | `629_search_index_change_workspace_index` |
| `563_search_index_change_changed_at_index` | `630_search_index_change_changed_at_index` |
| `564_issue_property_list_types` | `631_issue_property_list_types` |
| `565_channel_typing_cleanup` | `632_channel_typing_cleanup` |
| `566_channel_typing_reaction_id_idx` | `633_channel_typing_reaction_id_idx` |
| `567_channel_typing_reaction_retry_idx` | `634_channel_typing_reaction_retry_idx` |
| `568_channel_typing_reaction_gc_idx` | `635_channel_typing_reaction_gc_idx` |
| `569_channel_typing_limits` | `636_channel_typing_limits` |
| `570_channel_typing_quota_idx` | `637_channel_typing_quota_idx` |
| `571_channel_typing_expiry_idx` | `638_channel_typing_expiry_idx` |
| `572_channel_typing_abandoned_idx` | `639_channel_typing_abandoned_idx` |

## Validation

Runs use independent local PostgreSQL databases and the repository CLI guard. Node tests disable Node 25 experimental webstorage. Release and deployment are outside this request.

- Go: complete `./...` run passed 70 packages; the two failed packages were corrected and fully rerun. `cmd/server`, `internal/handler`, and the subsequently changed `internal/service` now pass their complete package suites. Agent catalog and migration packages also pass full tests under the CLI guard.
- Shared core: 173 files, 2,264 tests passed.
- Shared views: 494 files, 6,240 tests passed.
- Desktop: 71 files, 773 tests passed.
- Web: 37 files, 280 tests passed.
- Workspace typecheck (web, desktop, core, views and auxiliary packages) passed; mobile typecheck passed separately.
- Private contract script passed with PostgreSQL available; database checks were executed, not skipped.
- Fresh schema migration, published-version upgrade, immutable published migration comparison, matching migration directions, migration-hook tests and private inventory guard tests passed.
- `git diff --check` passed. No production deployment or release packaging smoke was run.

The original main checkout remains untouched. Private main advanced during this synchronization; its `1550bd456` delta was integrated and verified in this worktree, with its ancestry recorded in the completed branch history.
