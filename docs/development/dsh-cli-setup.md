# DSH CLI setup in MissionOS Desktop

The current machine's **Runtimes → Machine detail** header includes a **DSH CLI** button immediately before **Rename machine** on desktop. It opens a dialog and checks the CLI on demand. The old Local daemon settings entry has been removed.

The button is only rendered for the current desktop machine, including its stopped-daemon placeholder. It does not appear on other machines or the web app. Detection does not run merely because the machine page is displayed.

- Check again detects the current installation and command state.
- If DSH Desktop is absent, MissionOS only prompts the user to install it. It does not download DSH, install an npm CLI, or alter shell startup files.
- If DSH Desktop is present but its command is missing or broken, the explicit **Install / repair CLI** action invokes that installation's bundled command manager.
- Existing PATH commands, including Homebrew or user wrappers, are probed first and accepted without registration or replacement when compatible. A different file path alone is not a command conflict.
- A failed custom-command probe never triggers replacement. A failed DSH-owned registered launcher may still be repaired through the bundled worker after ownership checks.
- A ready result requires the selected launcher to return the DSH `multica` profile's version-1 discovery frame, not merely a successful command registration.
- Restart the local daemon after successful registration to refresh discovered runtimes. MissionOS does not automatically interrupt running work.

## Supported locations

macOS: `/Applications/DeepSeek Harness.app` and `~/Applications/DeepSeek Harness.app`.

Windows: `%LOCALAPPDATA%\Programs\DeepSeek Harness`, `%ProgramFiles%\DeepSeek Harness`, and `%ProgramFiles(x86)%\DeepSeek Harness`.

A custom installation outside these locations is not considered proof that DSH is absent; the UI explicitly says it was not found in default locations and directs users to DSH's own command menu. Installed versions without `runtime/cli/command-manager.js` are shown as unsupported, not uninstalled. Linux command repair is not supported by the DSH worker.

## Safety boundaries

Only the trusted main window can request command management. The renderer cannot supply executable paths, worker operations, or elevation scripts. Installation is user-triggered and serialized; the DSH worker's inspected fingerprint confirms each mutation, preserving its stale-state and ownership checks. Other command installations and commands shadowing DSH are not overwritten. Probe failure, command ownership conflict, unsuccessful registration, and denied registration permission have separate UI messages; raw command output and exception details are not sent to the renderer. On macOS, a permission failure invokes the native authorization prompt using fixed installation paths. Cancellation is not treated as success.

Windows registry PATH changes do not update the running MissionOS process automatically. Verified launcher directories are appended to its child-process PATH without reordering existing entries. This is process-local and does not independently write registry PATH values.

Unit tests inject fake discovery and worker execution. Default tests do not execute user-installed agent CLIs. Windows registration and macOS authorization still require manual installed-app acceptance testing; normal source tests cannot establish native OS prompt behavior.
