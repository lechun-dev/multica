# DingTalk notification module

This directory is an isolated notification module. It translates a
stable `MentionCreated` event into delivery intents without importing Multica
business packages. Tests can run entirely with memory/mock implementations;
the host enables real member notifications automatically when the three
required DingTalk application credentials are configured.

## Development

Unit tests use the in-memory store and provider, so they never send to DingTalk.

```bash
cd extensions/dingtalk-notify && go test ./...
```

实际配置文件是项目根目录的 `.env`（即 `multica/.env`），不是本目录下的 `.env`。
本目录的 `.env.example` 仅用于查看变量清单，不能作为运行时配置文件；请不要在
`extensions/dingtalk-notify/` 下另建 `.env`。如果根目录还没有 `.env`，请在
`multica/` 目录执行 `cp .env.example .env`，然后只编辑根目录 `.env` 中的钉钉区块。
项目根目录的 `.env.example` 和 `docker-compose.selfhost.yml` 会透传同一组变量。
成员通知没有额外的开关或运行模式；`DINGTALK_CLIENT_ID`、
`DINGTALK_CLIENT_SECRET`、`DINGTALK_ROBOT_CODE` 三项完整后自动启动，缺少任意
一项则保持关闭。`DINGTALK_NOTIFY_WORKER_INTERVAL` 和
`DINGTALK_NOTIFY_MAX_ATTEMPTS` 只用于调整 Worker 行为。

The schema remains outside the host migration ledger. Review it with:

```bash
make dingtalk-notify-schema
```

When a staging database is available, the files can still be applied manually
in order with:

```bash
make dingtalk-notify-migrate ENV_FILE=.env
```

The command runs the ready-queue index in a separate `psql` invocation because
it uses `CREATE INDEX CONCURRENTLY`. When DingTalk OAuth credentials are
configured, the Multica server also calls `EnsureSchema` during startup. The
module takes a PostgreSQL advisory lock, applies its embedded migrations on a
dedicated connection, and keeps the concurrent index outside a transaction.
If migration fails, only DingTalk login is disabled; the server does not expose
the database error to the browser.

## Contract

`event.schema.json` is the only event shape the future Multica adapter needs to
publish. The adapter should map `comment:created` mention data into this shape,
then call `BuildMessages` and enqueue the returned messages through `Store`.
`MemoryStore` is provided for tests; production can implement the same
small interface with PostgreSQL. No comment, Inbox, Agent, or database package
is imported by this module.

`EventAdapter` is an optional host-neutral boundary for callers that need an
explicit adapter switch. The current Multica host subscribes directly after
the required application credentials pass startup validation. The isolated SQL lives in
`migrations/001_dingtalk_notify.sql` and
`migrations/002_dingtalk_notify_outbox_ready_idx.sql`; the host invokes only
the module-owned `EnsureSchema` entry point.

Routing is intentionally explicit:

- A member target is sent to the active DingTalk user binding as a P2P message.
- Agent notifications are deferred by default. The host must explicitly enable
  them, and every selected channel must carry its own `robot_code`; there is no
  fallback to a deployment-wide/default Bot. Missing configuration is recorded
  as a skipped delivery and never sends to DingTalk.
- Unbound, disabled, or unmatched member targets become `failed` deliveries and
  do not trigger another DingTalk notification.

## Reliability and identity contracts

- `EnqueueMessages` derives a stable SHA-256 idempotency key from the source
  event and destination, so duplicate event delivery does not duplicate a
  DingTalk message.
- `Worker.RunOnce` claims due outbox rows, sends each independently, retries
  only `RetryableError` values with exponential backoff, and marks permanent
  failures for audit. It never runs in the comment HTTP request.
- `OAuthService` binds the DingTalk identity to the already authenticated
  `(workspace_id, member_id)` using a single-use, expiring state. The separate
  `LoginOAuthService` only verifies an unauthenticated login identity; the host
  must apply its trusted account-matching policy and issue the Multica session.
- `MemberBinding.Groups` is optional. Without an explicit group intent a
  member always receives a P2P message; with intent, configured groups are
  selected and deduplicated. Agent targets remain Bot-only.

## Host integration surface

The host keeps its existing comment path unchanged and mounts the module at the
event boundary:

1. Convert the host's `comment:created` payload to `CommentMention` with
   `AdaptCommentMention`, then call `EventAdapter.PublishMention`.
2. Resolve bindings/channels with `SQLBindingStore` and
   `SQLAgentChannelStore`, call `BuildMessages`, and enqueue messages in
   `SQLStore`. The adapter is the only event seam; it never sends HTTP inline.
3. Run `Worker.Run` under the host supervisor. It leases rows with
   `FOR UPDATE SKIP LOCKED`, records an optional `AuditSink`, retries only
   `RetryableError`, and survives process restarts.

The current Multica host adapter is intentionally thin: it registers public
`/auth/dingtalk/start` and `/auth/dingtalk` routes, persists OAuth state and
login identities in the module tables, subscribes to `comment:created`, resolves
member bindings from the module-owned identity table, and sends through the
deployment-wide DingTalk application. This member path does not depend on
`MULTICA_DINGTALK_SECRET_KEY`, `channel_installation`, or an Agent-owned Bot.
Agent-owned notifications are routed by the host bridge to the owner's member
binding when enabled. Production workers use `SQLStore`; tests may use
`MemoryStore`.
The login app needs DingTalk permission to resolve a user's `unionId` to the
enterprise `userId` and read the enterprise member profile/email. Production
OAuth callbacks must use the exact HTTPS URL registered in DingTalk, for
example `https://multica.example.com/auth/dingtalk/callback`.

The host bridge reads `DINGTALK_NOTIFY_AGENT_OWNER_MENTIONS` (default `true`)
to enable or disable P2P notices when another member explicitly mentions an
Agent. The Agent owner's own member or Agent comments are suppressed.

## 任务权限申请通知

权限申请和审批结果同时写入 MissionOS 机器人通知队列及个人 1v1 队列。
机器人沿用上述三项应用配置和现有 Worker，不依赖申请人绑定个人钉钉身份；
接收人须为未禁用的当前工作区成员，且具有有效的通知身份绑定。
通知显示申请人姓名、中文权限名称、任务编号及名称，不显示用户 ID。
申请卡片提供“查看并处理申请”按钮，定位到已有的权限申请处理界面；
点击链接本身不授予权限。结果卡片提供“打开任务”按钮。

两条队列在权限申请事务内分别隔离失败，随事务一起提交或回滚。
机器人复用现有队列的去重及重试，不通过收件箱事件额外转发，也不会自动补发历史通知。
配置不完整或接收人无有效绑定时不入机器人队列，并记录跳过原因。
测试仅使用本地测试数据库、内存实现或模拟接口，不发送真实钉钉消息。

## 本机 DWS 个人私信

桌面端登录的成员在评论中使用 `mention://member/<user-id>` 提及另一位成员时，
服务端除保留原有机器人通知外，还会创建一条独立的个人消息 Outbox。只有人类成员
撰写的评论会进入该队列；Agent、system、自己提及自己以及同一评论中的重复提及均不
发送个人私信。用户可在通知设置中关闭“@成员时发送钉钉私信”，该开关默认开启。

个人消息由发送者自己的桌面端 Daemon 领取，并调用本机已登录的 `dws`：

```bash
dws chat message send --user <recipient-user-id> \
  --title "MissionOS 通知" --content "<markdown>" \
  --idempotency-key <stable-key> --ai-tag=false --format json
```

Daemon 会先运行 `dws auth status --format json` 和
`dws contact user get-self --format json`。身份优先按 `unionId` 核对；缺少
`unionId` 时必须同时匹配 `corpId + userId`。DWS 未安装、未登录或身份不一致时
禁止发送，消息保留在队列中并在桌面端提示处理；电脑离线后上线也会补发。队列默认
24 小时过期。发送命令返回 `openTaskId` 后，Daemon 会保存该检查点并调用
`dws chat message query-send-status`，只有拿到 `openMessageId` 才标记已送达。
WebSocket 仅发送“有待领取消息”的用户级提示，不携带消息正文。

桌面端还暴露统一的 `window.dwsAPI.ensureAuthenticated({ source })` 前置检查。
后续日历、文档、待办等 DWS 功能应仅在用户实际触发操作时调用它：已登录时直接
继续，未安装或未登录时复用同一个授权弹窗，不在应用启动或浏览普通页面时打扰用户。

首阶段只支持评论中的成员提及和 Markdown 文本私信；Agent 发言、任务详情等其他
场景，以及文件、图片、群聊和 DING 均不在本阶段范围内。单元与集成测试使用假 DWS，
不会真实发送钉钉消息。

The host also publishes terminal `task:completed` events. Each completed Agent
run is sent as a separate P2P completion notice to the Agent owner and the
human who initiated that run; duplicate identities are collapsed, and missing
DingTalk bindings are recorded as skipped deliveries. Completion notices use a
different message title (`✅ **MissionOS Agent 已完成执行**`) from mention notices. The
completion action uses the same `打开任务并回复` link as mention notices so
users can open the task directly in DingTalk's side panel.

The module includes host-neutral HTTP handlers for the remaining management
surface:

- `OAuthHTTPHandler`: `/dingtalk/oauth/start`, `/dingtalk/oauth/callback`,
  `/dingtalk/binding`, `/dingtalk/binding/revoke`, and an explicit
  `/dingtalk/binding/test` message.
- `ChannelHTTPHandler`: agent Bot/channel list, upsert, and deactivate.
- `DeliveryHTTPHandler`: workspace-scoped delivery status and manual retry.

The host must authenticate the request and inject an
`AuthenticatedIdentity` context value before dispatching these handlers.
`DingTalkOAuthProvider` implements the existing OAuth application flow, and
`DingTalkProvider` implements P2P and Bot group sends with token caching,
401 refresh, 429/5xx/timeout classification, and the documented DingTalk
Open API payloads. Notifications with a task reply URL use the single-button
`sampleActionCard` message type (`title`, `text`, `singleTitle`, and
`singleURL`). Messages without a reply URL continue to use `sampleMarkdown`.
If an older robot explicitly rejects `sampleActionCard`, the provider retries
that message as `sampleMarkdown`; other failures retain their normal retry
classification.

`migrations/001_dingtalk_notify.sql` creates only module tables. The ready
queue index is in `002_dingtalk_notify_outbox_ready_idx.sql` because the
repository requires every index build to use `CREATE INDEX CONCURRENTLY`.
There are no database foreign keys; cleanup and workspace authorization stay
in the application layer.

本模块的单元测试不会连接生产数据库或发送真实钉钉消息。部署前，请先在项目根目录
配置所需的基础设施变量，并填写
`DINGTALK_CLIENT_ID`、`DINGTALK_CLIENT_SECRET`、`DINGTALK_CORP_ID`、
`DINGTALK_OAUTH_REDIRECT_URI`、`DINGTALK_AGENT_ID`、`DINGTALK_ROBOT_CODE`、
`DINGTALK_API_BASE_URL`、`DINGTALK_OAUTH_AUTH_URL`。OAuth token 和用户信息接口
使用代码内置的钉钉官方默认地址，无需配置环境变量；只有未来需要接入代理或特殊网关时，
才应在代码层显式覆盖。Client Secret 和加密密钥只应通过本机 `.env` 或密钥管理器注入。

## Departure-aware directory sync

The host's existing organization-sync action uses the same configured application
credentials to call `GET /v1.0/contact/empLeaveRecords` (`ListEmpLeaveRecords`).
Enable the corresponding departure-record read permission on the DingTalk
application; personal HR-roster permissions do not establish application access.
The request/query/header/response contract follows Alibaba's official
`@alicloud/dingtalk` 2.2.48 `contact_1_0` SDK. No SDK dependency is installed.

For the manual HTTP action, only a human workspace owner can trigger account suspension. Self, other owners,
and accounts outside the operator's owned workspaces remain protected. The host
matches existing active DingTalk user-ID bindings, never names or email, and skips
users resolved in the current directory snapshot (including rehires). It never
re-enables an account, deletes business history, or treats a missing directory
member as proof of departure.

The host reads from one day before the oldest existing linked local account to
the sync start, splitting the range into 30-day windows and following every page.
No suspension runs unless the whole departure read succeeds. On application
permission, malformed-response, or pagination errors, the normal directory sync
can still finish but returns `departure_sync_status: unavailable`. An admin or
machine actor gets `owner_required` and performs directory-only sync. New clients
show these states explicitly; an older server response defaults to `not_checked`.
No additional migration is needed beyond the account-access migration 602.

Regression checks use mocked DingTalk endpoints and the dedicated integration
database; they do not call live DingTalk or suspend production accounts.

### Daily refresh (MissionOS server)

The server registers `dingtalk_directory_daily` when the DingTalk adapter is
configured and project authorization is enabled. It runs at **03:00 Beijing time
(UTC+08:00) every day**, using the application credentials above, not a logged-in
person's DWS authorization. `DINGTALK_DIRECTORY_SYNC_ENABLED=false` disables this
job at server startup without disabling manual refresh or DingTalk login.

Only existing workspaces with DingTalk organization records are enrolled; the job
does not import the enterprise directory into unrelated workspaces. The first
currently enabled workspace owner (ordered by membership creation time and ID)
provides the account-management authorization ceiling, not provider credentials.
No enabled owner means a failed audited run without any provider calls or writes.
The job rechecks this authority after provider reads and retains every manual
self/owner/cross-workspace protection. Protected accounts are counted as skipped,
not silently suspended. Re-enabling remains a manual owner action.

The existing PostgreSQL `sys_cron_executions` ledger provides cross-replica daily
slot deduplication, leases, heartbeats and restart recovery. One independent
worker avoids delaying other scheduled server jobs during directory pagination.
A missed schedule catches up to the latest due daily slot, not every historical
day, and a successful slot is not replayed on each restart. Each occurrence has
at most **3 attempts**, with **5-minute and 30-minute** retry delays, a 20-minute
run timeout and a 25-minute stale lease. After retries are exhausted, the next
daily slot can run normally.

A departure-read error still permits ordinary directory refresh but marks the
scheduled run failed and eligible for retry; no account is suspended from partial
departure results. Invalid/empty directory snapshots are never persisted. Manual
and scheduled snapshot writes are transaction-serialized per workspace. Successful
commits publish `organization:synced`, invalidating only the affected workspace's
organization/permission/list read generations, including background writes that
bypass HTTP middleware. Tasks, projects and business history are not deleted.

Inspect recent runs without employee data or credentials:

```sql
SELECT scope_id AS workspace_id, plan_time, status, attempt, next_retry_at,
       rows_affected, result, error_code
FROM sys_cron_executions
WHERE job_name = 'dingtalk_directory_daily'
ORDER BY plan_time DESC, scope_id
LIMIT 30;
```

Coverage: `TestDingTalkDirectoryDaily*`,
`TestDingTalkDirectorySyncEventInvalidatesWorkspaceCaches` and existing manual
directory/account-access regressions. Fixtures use fake DingTalk
endpoints and the local integration database, never production account writes.
