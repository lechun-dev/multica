# Official DeepSeek Harness integration

## Approved scope (2026-10-08)

MissionOS uses the official DSH ACP profile (`dsh --profile acp`), not the
custom `multica` bundle. Detection, model discovery, execution, configuration,
resume, cancellation and shutdown must use the same protocol.

Desktop automatically selects an existing compatible CLI or the official
App's bundled launcher before starting its local daemon. This selection is
process-local (`MULTICA_DSH_PATH`); it never replaces commands, edits system
PATH, elevates privileges, patches the App, or installs third-party plugins.
An explicit operator override remains authoritative. No installed launcher
means an installation prompt, not a download. The header dialog performs a
bounded ACP initialize check and reports timeout/protocol/startup failures
without exposing subprocess output or credentials.

Official App locations: `/Applications/DeepSeek Harness.app`,
`~/Applications/DeepSeek Harness.app`, and Windows DeepSeek Harness under
LOCALAPPDATA/Programs or ProgramFiles. Other locations can use
`MULTICA_DSH_PATH` or an existing PATH command. Linux supports existing CLIs.

## Implementation and acceptance sequence

1. Replace custom JSONL execution/catalog with existing shared ACP transport,
   MCP conversion, deliverable tracking, usage and session error handling.
   Apply model/effort through advertised config option IDs, validating replies.
2. Remove daemon profile-manifest gating and automatic plugin installation.
   ACP initialize is the availability check; failed probes are not proof that
   user configuration needs replacement. Prefer official bundled launchers.
3. Replace desktop command registration/elevation with automatic process-local
   launcher selection, bounded initialization, and truthful localized status.
4. Fake-process tests first; then focused/full Go and desktop checks. Explicitly
   authorized real-agent acceptance uses `agentintegration` and
   `MULTICA_RUN_REAL_AGENT_SMOKE=1`, temporary cwd, normal user authorization,
   and no credential output. Verify discovery, prompt, resume and cancellation.
5. Review, merge to main, push and create a new prerelease tag only after
   acceptance. Do not bypass release quality gates.

## Migration and safety

No legacy adapter is retained. Existing custom multica session IDs may be
rejected by ACP; a genuine resume rejection is reported so the daemon can
start fresh. History is not claimed to be converted. Existing DSH profiles and
credentials remain untouched. ACP owns its session persistence; the former
MULTICA_DSH_SESSION_ROOT override is not an official ACP setting and is removed.
MULTICA_DSH_PROFILE_BUNDLE and MULTICA_DSH_PLUGIN_PATH no longer install anything.
Login/API credentials still require normal DSH authorization.

### Reusing the official Desktop connection

The official App's settings form writes profile-local overrides. Launching
`--profile acp` does not inherit `profiles/desktop/cordis.patch.yml`, even though
both profiles share the official credential store. Using a gateway key against
the default official API can therefore look like an invalid key.

Before discovery or execution, read the current DSH home's Desktop patch
(DSH_HOME, otherwise ~/.dsh), capped at 1 MiB. Copy only literal `baseURL` and
`apiKeyEnv` fields for `llm-deepseek`, applying sequential overrides. Pass that
pair through the official `--patch` option in a private temporary overlay,
removed after process shutdown and on startup failure. Desktop's explicit
connection takes precedence over ACP/home connection overrides for this launch.
No App/profile files are modified and no unrelated Desktop plugins are loaded.

The key value is never read or copied by MissionOS. Official DSH resolves the
named credential, normally DEEPSEEK_API_KEY, using its normal environment/store
precedence. A different DSH_HOME selects a different official credential store.
With no Desktop connection override, official ACP defaults remain unchanged.
Custom credential references require an explicit Desktop API URL so an endpoint
and credential are not silently paired from different profiles. Malformed or
dynamic connection fields fail closed with value-free errors.

Contract references: official `packages/settings/settings/README.md`,
`packages/credentials/credentials-local/README.md`,
`packages/llm/llm-deepseek-api-key/README.md`, and
`apps/cli/src/profile-boot.ts` (`--patch`).

Primary contract: official DeepSeek Harness `packages/acp/acp/README.md`,
`packages/bundle/acp-app/README.md`, and the installed official App's ACP
handshake. Model IDs are opaque values from configOptions, not reconstructed
provider/model strings. Sessions receive task cwd and MCP servers directly.

## Local verification (2026-10-08)

- Official macOS App: DeepSeek Harness 0.2.0-rc.2. Real launcher selection
  returned ready in 610ms through the existing PATH wrapper and 701ms through
  the bundled launcher with a restricted PATH. PATH was unchanged in both cases.
- The opt-in official configuration smoke passed: initialize, three advertised
  model choices, confirmed model/effort selection, close, cross-process resume
  (whose response omits sessionId), and rejection of a nonexistent session.
  This test uses isolated temporary DSH_HOME/cwd and makes no model requests.
- Desktop: 70 test files / 756 tests passed; node/web and shared views type
  checks passed; lint passed with one existing tab-content.tsx dependency
  warning; the desktop build passed with existing bundler warnings.
- DSH/shared ACP/Qoder and daemon regression checks passed with the race
  detector and the real-agent CLI guard. The full daemon suite passed.
  A regression test first reproduced a resumed session losing its ID on
  configuration failure; the fix now preserves it without sending a prompt.
- The complete default Go verification wrapper passed with the race detector
  and an isolated CODEX_HOME/sessions directory. No tests were excluded and no
  installed agent CLI was invoked. The wrapper's own regression tests passed.
- Two Codex cancellation checks initially failed on both the unchanged baseline
  and this branch. Their fake executions fell back to the ambient session
  history scan. With an isolated CODEX_HOME containing an empty sessions
  directory, both baseline checks passed three consecutive rounds. No Codex
  production code, test assertions, or release gate was changed.
- Go vulnerability scanning reported no vulnerabilities. The production
  dependency audit still reports 181 findings (3 critical, 66 high, 96 moderate,
  16 low); dependency manifests and the lockfile are unchanged by this migration.
- The real dialog was previewed at desktop/mobile widths for readiness,
  timeout, and recheck behavior using a mock-status browser harness. This is
  distinct from the real launcher checks above, not a full installed desktop
  end-to-end test. Native Windows acceptance was not run on this macOS host.

### Desktop connection acceptance

The initial invalid-key diagnosis was incomplete: ACP did not inherit the
Desktop gateway URL. With the connection pair reused, the next attempt reached
the gateway but its account group did not support ACP's default advisory model
deepseek-v4-flash. The App was configured for deepseek-flash. Selecting its
advertised opaque ACP ID explicitly made real model output and contextual
cross-process resume pass without changing the user's URL, key or App settings.
The opt-in smoke accepts MULTICA_DSH_SMOKE_MODEL only if ACP advertises that ID;
it does not rewrite the product default or silently retry other models.

The final opt-in smoke passed in 6.27s: real model output, contextual
cross-process resume, and bounded cancellation of a long pending prompt after
session setup. An earlier attempt to cancel on the first text update received
a completed result; this gateway can deliver text only at the end. The final
test cancels while waiting for the long response, not after buffered text arrives.
The default fake test independently verifies session/cancel and session/close
requests after session setup rather than relying on a 300ms startup deadline.

Connection regressions passed three consecutive rounds, including duplicate
overrides, literal-only parsing, value-free validation errors, private overlays,
cleanup after execution/discovery/failed spawn, and no secret/unrelated-setting
copying. The complete default Go wrapper passed again with the race detector
and isolated CODEX_HOME/sessions; no packages or tests were excluded. The wrapper
regression tests passed and Go vulnerability scanning again found no vulnerabilities.
The desktop build was repeated with the updated embedded CLI and passed; the
focused launcher/dialog suite also passed (2 files / 23 tests).
No credential value is recorded here. The installed MissionOS Preview was not
replaced during these checks; native Windows and full installed-App E2E remain
outside this macOS acceptance.

## Default model and discovery follow-up (2026-10-09)

The official Desktop `agent-default-model` route does not automatically apply
to the ACP profile: ACP has an explicit `acp` provider/model configuration.
MissionOS now maps only Desktop's literal provider/model fields to that row
in the same private launch overlay used for the connection pair. It does not
copy unrelated plugins or credentials, alter official App settings, or invent
model IDs. Desktop reasoning effort is not copied because the ACP launch
configuration does not support that field; explicit effort choices still use
the advertised ACP session configuration options.

The runtime version displayed by ACP is its adapter version, not the installed
App's version. MissionOS labels it `ACP` to avoid implying that an official
App reporting adapter version `0.0.1` is the wrong installation.

Verification on this macOS host:

- The installed official App at `/Applications/DeepSeek Harness.app`
  (0.2.0-rc.2) passed the opt-in `TestDshRealDesktopDefaultSmoke` in 2.46s.
  Discovery advertised the Desktop-configured model as default, and execution
  with no model/effort override returned the exact expected reply.
- The installed App also passed `TestDshRealRuntimeSmoke` in 5.34s: real model
  output, contextual cross-process resume, and bounded active-turn cancellation.
- The complete guarded default Go suite passed with the race detector and an
  isolated `CODEX_HOME/sessions`. Default tests did not invoke installed agents.
- Both model-picker component suites passed (2 files / 6 tests); focused lint
  and shared views typechecking passed. Discovery failure is now distinguished
  from a successful empty catalog, without changing saved/manual selections.
- The desktop build, including its freshly bundled daemon CLI, passed. This
  verifies compilation, not installation or full desktop end-to-end behavior.
- Full installed MissionOS E2E is **not accepted yet**: staging model discovery
  remains pending until its 30s timeout. A controlled request was delivered
  by HTTP heartbeat, while a fresh WebSocket heartbeat returned a normal ack
  without delivering its pending request. These diagnostics isolate a transport
  discrepancy but do not establish its deployment/proxy/store root cause.
  Diagnostic claims were closed with an intentional failure or expired; they
  are not real model-list successes. No server configuration was changed.

Do not treat source-level smoke success as a fix for the staging dispatch
problem, or publish a production tag before desktop end-to-end acceptance.
