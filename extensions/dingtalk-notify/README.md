# DingTalk notification module

This directory is an isolated, opt-in notification module. It translates a
stable `MentionCreated` event into delivery intents without importing Multica
business packages or making network calls. The production adapter can be
connected later through the documented interfaces.

## Development mode

Use `local/mock` while the server and database are still being provisioned.
The mock provider records messages in memory and never sends to DingTalk.

```bash
go test ./extensions/dingtalk-notify/...
```

Configuration is described in `.env.example`. Empty infrastructure values are
allowed only in `local/mock`; staging and production startup should validate
them before enabling delivery.

## Contract

`event.schema.json` is the only event shape the future Multica adapter needs to
publish. The adapter should map `comment:created` mention data into this shape,
then call `BuildMessages` and persist returned failed deliveries in its own
audit/outbox store. No comment, Inbox, Agent, or database package is imported
by this module.

Routing is intentionally explicit:

- A member target is sent to the active DingTalk user binding as a P2P message.
- An agent target is sent only to active Agent Bot channels. It never falls
  back to the owner. A group intent (`发群`, `群里`, `群消息`) selects all active
  channels; otherwise only channels whose configured name is present in the
  text are selected.
- Unbound, disabled, or unmatched targets become `failed` deliveries and do
  not trigger another DingTalk notification.
