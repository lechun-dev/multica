# newDingSDK workspace task reads

newDingSDK accepts an existing user API key, resolves its trusted DingTalk identity, and calls the signed exchange below. It uses `/internal/agent/issues` and `/internal/agent/issues/{id}`, which delegate to the existing task handlers with a five-minute workspace-bound read JWT. `/internal/agent/members` resolves member names to assignee user UUIDs; `/internal/agent/projects` resolves project titles to project UUIDs.

## Configuration and identity

Set `MISSION_EXCHANGE_SECRET` to the same strong random value on MissionOS and newDingSDK (at least 32 characters). Keep it separate from `JWT_SECRET`. An empty/short secret disables exchange with HTTP 503. Existing production JWT configuration remains required.

Redis is mandatory for exchange: all replicas must share its nonce store. Missing/unavailable Redis returns 503; no process-local fallback is used. Keep host clocks synchronized. Limit exchange access at the gateway/network layer to newDingSDK where possible, and redact authentication headers and bodies from logs.

The optional DingTalk extension's `dingtalk_notify_identities` table must already exist. Exchange requires an exact active `ding_user_id` mapping to one valid existing MissionOS user, and existing membership in the requested workspace. `login_only` identities are eligible. There is no automatic provisioning, email matching, PAT generation/storage, or new credentials table. Missing binding/nonmember/disabled user returns 403; ambiguous or invalid binding returns 409; unavailable storage returns 503.

## Signed exchange

```http
POST /internal/auth/dingtalk/exchange
Content-Type: application/json
X-Mission-Timestamp: <UTC Unix seconds>
X-Mission-Nonce: <16 random bytes, hex>
X-Mission-Signature: <HMAC-SHA256 hex>

{"ding_user_id":"trusted-id","workspace_id":"workspace-uuid","purpose":"agent_task_read"}
```

Signature input is the exact method, request URI, timestamp header, nonce header, and raw body separated by newline:

```text
POST\n/internal/auth/dingtalk/exchange\nTIMESTAMP\nNONCE\nRAW_BODY
```

The body limit is 4096 bytes. Timestamp tolerance is ±60 seconds; Redis `SETNX` retains nonce keys for two minutes. Unknown JSON fields and trailing JSON are rejected. Only server-resolved identities may be sent; never forward an agent-supplied DingTalk ID.

Response includes `access_token`, `expires_in: 300`, `workspace_id`, `user_id`, and current workspace `role`, with `Cache-Control: no-store`. Tokens have `source=lechun_mission_read`, issuer `missionos`, audience `newDingSDK`, and no browser session ID. Purpose-specific tokens have task-read or owner-read scope. They are non-renewable, bearer-only, and accepted only for GET internal task list/detail and the exact internal members/projects endpoints in the workspace selected by `X-Workspace-ID`. A mismatching workspace query, slug selector, Cookie token, write, nested/static task route, ordinary member-management route, or daemon route is rejected. Session parsing/renewal and the independent WebSocket authentication entry point also reject these tokens.

newDingSDK must not persist, expose, or log these tokens. It exchanges on each request and uses independent cursor signing; neither secret is a new user API key.

## Existing permissions

`ListIssues` and `GetIssue` remain authoritative. Internal agent handlers re-read workspace membership on each request. Only a current workspace owner with owner-read scope bypasses project/task ACLs on the internal task and project read routes, independently of `PROJECT_OWNER_BYPASS_ENABLED=false`. Normal browser reads and `/api/issues` continue to follow the existing switch. Other workspace roles follow existing effective task/project permissions. Cloud issue-window restrictions still apply. Unauthorized detail keeps existing 403/404 behavior without task content.

No copied ACL is maintained in newDingSDK; future changes to these handlers' permission decisions are inherited. Changes to middleware or external status contracts still require integration tests.

## Member-name lookup and assignee enrichment

`GET /internal/agent/members?workspace_id=UUID&q=NAME&limit=50&offset=0` uses the same purpose-specific authorization and current workspace membership check. `q` is optional, trimmed, case-insensitive name substring search (1–100 Unicode characters when supplied). `limit` is 1–100; `offset` is a nonnegative int32. Results are sorted by user UUID and return only `members: [{user_id, name}]` and filtered `total`. Duplicate names are retained. Email, role, DingTalk ID and member-record ID are not exposed.

This directory is workspace-membership scoped, not derived from visible tasks. Retained member relationships may include users who have not logged in or whose accounts are disabled; listing a name does not grant that user login or task access. The returned `user_id` is the member-type task `assignee_id`, not `member.id`.

Internal task pages/details additionally project optional `assignee_name` using one workspace directory query per page. Normal API, write and event projections are unchanged. newDingSDK returns this as `assignee.name`, or null for missing/departed members and agent/squad assignees. No new schema or stored name copy is introduced.

The SDK public lookup is `GET /lechun-ding/chats/mission/members`, accepting `workspace_id`, `q`, `limit` and a signed `cursor`. Member and task cursors are resource-separated and not interchangeable. Agents must page all name matches, disambiguate duplicate names, then call the task list with `assignee_type=member&assignee_id=USER_UUID`; task authorization still uses the caller's identity.

## Project-title lookup

`GET /internal/agent/projects?workspace_id=UUID&q=TITLE&limit=50&offset=0` accepts the same directory parameters as members. It returns only `projects: [{id, title}]` and the authorized project `total`. It rechecks current workspace membership, uses the existing `ProjectAuth.ScopeWithWorkspaceOwned` visibility service for nonowners, and bypasses project ACLs only for a currently validated owner with owner-read scope. The normal browser/API owner switch remains unchanged. Project visibility is not inferred from task results; a task-only grant need not grant project-directory visibility.

Visibility is filtered before title search, count and pagination. Search is a trimmed case-insensitive literal substring, UUID ordering is stable, duplicates are retained, and empty/completed visible projects are included. No project content, task counts or permission-management fields are returned. No new schema or environment configuration is required.

The SDK exposes `GET /lechun-ding/chats/mission/projects` with `workspace_id`, `q`, `limit` and signed `cursor`. Project, member and task cursors are independently bound and cannot be reused across resources/users/filters. Agents must read all candidate pages, prefer a unique exact name match, use a sole fuzzy candidate only when unambiguous, and ask the user on duplicates. Use the confirmed project `id` as task `project_id`, optionally together with resolved `assignee_id` and `assignee_type=member`. Never substitute task keyword search for project or assignee filtering.

This change adds no independent statistics endpoint or task-total projection. To answer a task-count question, page the authorized task list and deduplicate UUIDs; specify `archive_state=all` if archived tasks are intended. Counts remain best-effort under concurrent changes.

## Optional task read projections

The existing paginated list now supports independent `created_from/to` and `completed_from/to` RFC3339Nano ranges. Bounds are inclusive lower/exclusive upper and applied before authorized count/limit/offset. `sort=completed_at` puts NULL last with the existing stable UUID tie-breaker. `search_description=true` opts into description matching alongside existing title/identifier matching. The `open_only=true` unpaginated path rejects time/completion options instead of silently ignoring them.

List and detail accept `include_completed_at=true`. Only opted-in reads add `completed_at`; old reads and write/event responses omit it. Authorized pages are enriched in one workspace-scoped batch, not one query per task. Shared core schema/type accepts the optional nullable timestamp.

For currently effectively done tasks, completion is the latest `activity_log.status_changed` transition whose effective `from` is not done and effective `to` is done. Done-to-done transitions do not reset it. Reopened/cancelled tasks and missing history produce NULL. This is not `due_date` or an `updated_at` fallback, and requires no migration/backfill.

Activity listeners persist asynchronously, so completion reads/filters are eventually consistent with status writes. Failed/missing history is not reconstructed. Current status categories interpret historical status keys: changing a category can change the derived timestamp. Strict audit guarantees would require a separately approved durable-event design. Assess query plans for representative activity volume before rollout; completion filters/sorts use correlated history queries and authorized totals can be expensive.

newDingSDK exposes signed offset cursors, default 50/max 100 and 30-minute expiry, with `snapshot_consistency=best_effort`. This is not keyset pagination or a fixed snapshot; clients retain identical filters and deduplicate task UUIDs. Task output is allowlisted and excludes credentials, raw metadata, logs/files/comments.

## Verification and rollout

Use the repository-managed development/test environment (`make up`, `make status`, `make down`); do not test against production or manually create an assumed database. No new schema migration is required, but existing task-permission/status migrations and the optional identity extension are prerequisites.

```sh
cd server
go test ./internal/auth ./internal/middleware ./internal/realtime -count=1
go test ./internal/handler -run 'TestMission' -count=1 -v
go vet ./internal/auth ./internal/middleware ./internal/realtime ./internal/handler ./cmd/server
```

The handler suite skips when its database is unreachable. The exchange fixture additionally skips when the optional identity table is absent; skipped tests are not evidence of successful identity/ACL integration. Core schema tests are in `packages/core/api/schemas.test.ts`.

Before rollout, verify owner/member/revoked/cross-workspace reads, completion history and range boundaries, paging, malformed identities, replay/Redis outage and JWT route/write refusal in an authorized non-production environment. Deploy MissionOS support before enabling the SDK integration. Configuration changes, deployment, production checks and push require separate authorization. The public SDK contract and examples live in `newDingSDK/docs/mission-tasks.md` in the sibling project.

The two mapping lookups need no new environment variable. Deployment must include Go routing and exact web proxies `/internal/agent/members` and `/internal/agent/projects`; do not proxy their subpaths or all `/internal` routes. The external gateway must separately grant GET permission for `lechun-node/lechun-ding/chats/mission/members` and `lechun-node/lechun-ding/chats/mission/projects` to intended API keys. The existing SDK detail route remains `/lechun-ding/chats/mission/tasks/detail?taskId=TASK_UUID_OR_IDENTIFIER`.
