# DingTalk notification module

This directory is an isolated, opt-in notification module. It translates a
stable `MentionCreated` event into delivery intents without importing Multica
business packages. It can run entirely with memory/mock implementations while
the host's production database and secrets remain unconfigured.

## Development mode

Use `local/mock` while the server and database are still being provisioned.
The mock provider records messages in memory and never sends to DingTalk.

```bash
cd extensions/dingtalk-notify && go test ./...
```

实际配置文件是项目根目录的 `.env`（即 `multica/.env`），不是本目录下的 `.env`。
本目录的 `.env.example` 仅用于查看变量清单，不能作为运行时配置文件；请不要在
`extensions/dingtalk-notify/` 下另建 `.env`。如果根目录还没有 `.env`，请在
`multica/` 目录执行 `cp .env.example .env`，然后只编辑根目录 `.env` 中的钉钉区块。
项目根目录的 `.env.example` 和 `docker-compose.selfhost.yml` 会透传同一组变量。
基础设施变量在 `local/mock` 模式下可以留空；启用 staging/production 前必须补齐并通过启动校验。

The schema is intentionally not part of the host migration ledger while the
deployment database is still being provisioned. Review it with:

```bash
make dingtalk-notify-schema
```

When a staging database is available, apply the two files in order with:

```bash
make dingtalk-notify-migrate ENV_FILE=.env
```

The command runs the ready-queue index in a separate `psql` invocation because
it uses `CREATE INDEX CONCURRENTLY`. It never runs during normal Multica
startup.

## Contract

`event.schema.json` is the only event shape the future Multica adapter needs to
publish. The adapter should map `comment:created` mention data into this shape,
then call `BuildMessages` and enqueue the returned messages through `Store`.
`MemoryStore` is provided for local/mock; production can implement the same
small interface with PostgreSQL. No comment, Inbox, Agent, or database package
is imported by this module.

`EventAdapter` is the feature-flagged host boundary. Disabled mode returns
`ErrDisabled` without publishing; enabled mode only validates and forwards the
normalized event, so the existing comment path remains unchanged until the
host explicitly wires it. The isolated SQL proposal is in
`migrations/001_dingtalk_notify.sql`; it is not applied automatically.

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

The host should keep its existing comment path unchanged and mount the module
behind a feature flag:

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
the active DingTalk installation for the configured login app, and reuses the
built-in per-installation sender. Agent targets remain disabled by default.
Production workers use `SQLStore`; local/mock callers may use `MemoryStore`.

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
Open API payloads.

`migrations/001_dingtalk_notify.sql` creates only module tables. The ready
queue index is in `002_dingtalk_notify_outbox_ready_idx.sql` because the
repository requires every index build to use `CREATE INDEX CONCURRENTLY`.
There are no database foreign keys; cleanup and workspace authorization stay
in the application layer.

`local/mock` 模式不会连接生产数据库或发送真实钉钉消息。启用 staging/production
前，请先在项目根目录配置 `APP_BASE_URL`、`API_PUBLIC_URL`、`DATABASE_URL`、
`REDIS_URL`、`ENCRYPTION_KEY`、`SECRET_STORE_REF`、`DEPLOY_ENV`，并填写
`DINGTALK_CLIENT_ID`、`DINGTALK_CLIENT_SECRET`、`DINGTALK_CORP_ID`、
`DINGTALK_OAUTH_REDIRECT_URI`、`DINGTALK_AGENT_ID`、`DINGTALK_ROBOT_CODE`、
`DINGTALK_API_BASE_URL`、`DINGTALK_OAUTH_AUTH_URL`。OAuth token 和用户信息接口
使用代码内置的钉钉官方默认地址，无需配置环境变量；只有未来需要接入代理或特殊网关时，
才应在代码层显式覆盖。Client Secret 和加密密钥只应通过本机 `.env` 或密钥管理器注入。
