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

### Release hold

The real prompt smoke reached the official ACP default model but failed provider
authorization (invalid API key). No credential value is recorded here. The user
must update authorization inside the official App; CLI preparation cannot
replace or repair model credentials. Successful model output, contextual resume
after a real prompt, and real active-turn cancellation remain unverified.

Keep these changes on the existing codex/dsh-cli-setup branch. Do not merge main,
push a release, or create a prerelease tag until real-model acceptance succeeds.
Do not replace the user's installed MissionOS Preview during this blocked state.
