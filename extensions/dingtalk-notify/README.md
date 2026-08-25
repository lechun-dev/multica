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

配置见本目录的 `.env.example`。实际使用时只需保留运行开关和四个钉钉变量：
`DINGTALK_NOTIFY_MODE`、`DINGTALK_NOTIFY_ENABLED`、`DINGTALK_CLIENT_ID`、
`DINGTALK_CLIENT_SECRET`、`DINGTALK_OAUTH_REDIRECT_URI`、
`DINGTALK_ROBOT_CODE`。数据库连接复用项目根目录的 `DATABASE_URL`，接口地址和
重试参数使用代码默认值，不需要在通知模块里重复配置。

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
- An agent target is sent only to active Agent Bot channels. It never falls
  back to the owner. A group intent (`发群`, `群里`, `群消息`) selects all active
  channels; otherwise only channels whose configured name is present in the
  text are selected.
- Unbound, disabled, or unmatched targets become `failed` deliveries and do
  not trigger another DingTalk notification.

## Reliability and identity contracts

- `EnqueueMessages` derives a stable SHA-256 idempotency key from the source
  event and destination, so duplicate event delivery does not duplicate a
  DingTalk message.
- `Worker.RunOnce` claims due outbox rows, sends each independently, retries
  only `RetryableError` values with exponential backoff, and marks permanent
  failures for audit. It never runs in the comment HTTP request.
- `OAuthService` binds the DingTalk identity to the already authenticated
  `(workspace_id, member_id)` using a single-use, expiring state. It does not
  infer a Multica member from a DingTalk nickname or callback query.
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
前，请先在项目根目录配置 `DATABASE_URL`，并填写上述四个钉钉变量；Client Secret
只应通过本机 `.env` 或密钥管理器注入。
