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

The host also publishes terminal `task:completed` events. Each completed Agent
run is sent as a separate P2P completion notice to the Agent owner and the
human who initiated that run; duplicate identities are collapsed, and missing
DingTalk bindings are recorded as skipped deliveries. Completion notices use a
different message title (`✅ 智能体「…」已完成执行`) from mention notices.

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
