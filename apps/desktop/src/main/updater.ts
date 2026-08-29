import { autoUpdater, type UpdateDownloadedEvent } from "electron-updater";
import { app, net, session, type BrowserWindow, ipcMain } from "electron";
import type { IncomingMessage } from "node:http";
import { join } from "node:path";
import type {
  InstallUpdateResult,
  ManualUpdateCheckResult,
  UpdaterPreferences,
} from "../shared/updater-types";
import {
  DEFAULT_UPDATER_PREFERENCES,
  loadUpdaterPreferences,
  saveUpdaterPreferences,
  updaterPreferencesPath,
} from "./updater-preferences";
import {
  installMacUpdate,
  prepareMacUpdate,
  resolveMacUpdateUrl,
  selectMacUpdateFile,
  type DownloadedMacUpdate,
  type MacUpdateInfo,
} from "./macos-custom-updater";

// Silent background updates: electron-updater downloads on its own as soon
// as `update-available` fires; we only surface UI when the package is fully
// downloaded and ready to install on next quit.
// macOS uses a local replacement flow below because Squirrel.Mac rejects
// ad-hoc signed private builds before they can be installed.
const useMacCustomUpdater = process.platform === "darwin" && app.isPackaged === true;
autoUpdater.autoDownload = !useMacCustomUpdater;
autoUpdater.autoInstallOnAppQuit = !useMacCustomUpdater;

// Windows arm64 ships its own update metadata channel because
// electron-builder's `latest.yml` is not arch-suffixed on Windows — both
// arches would otherwise collide on the same file in the GitHub Release.
// See scripts/package.mjs (builderArgsForTarget) for the publish-side half
// of this pact. Pin the channel here so arm64 clients fetch
// `latest-lechun-arm64.yml` instead of the x64 metadata.
if (process.platform === "win32" && process.arch === "arm64") {
  autoUpdater.channel = "latest-arm64";
}

interface ChannelConfigurableUpdater {
  channel: string | null;
  allowDowngrade: boolean;
}

export function configureMacX64UpdateChannel(
  updater: ChannelConfigurableUpdater,
  platform: NodeJS.Platform = process.platform,
  arch: string = process.arch,
): void {
  if (platform !== "darwin" || arch !== "x64") return;

  // AppUpdater.channel enables allowDowngrade as a side effect. This channel
  // isolates a CPU architecture, not a release train, so preserve normal
  // monotonic version behavior after selecting the architecture feed.
  updater.channel = "latest-lechun-x64";
  updater.allowDowngrade = false;
}

// electron-builder does not architecture-suffix macOS update metadata.
// package.mjs publishes macOS x64 as `latest-lechun-x64-mac.yml`; the established
// arm64 feed and runtime path remain unchanged.
configureMacX64UpdateChannel(autoUpdater);

const STARTUP_CHECK_DELAY_MS = 5_000;
const PERIODIC_CHECK_INTERVAL_MS = 60 * 60 * 1000; // 1 hour
const ELECTRON_UPDATE_SESSION = "electron-updater";

type RendererChannel =
  | "updater:update-available"
  | "updater:download-progress"
  | "updater:update-downloaded"
  | "updater:update-error";

function isDestroyedObjectError(err: unknown): boolean {
  return err instanceof Error && err.message.includes("Object has been destroyed");
}

function sendToLiveRenderer(
  win: BrowserWindow | null,
  channel: RendererChannel,
  payload: unknown,
): void {
  if (!win || win.isDestroyed()) return;

  try {
    const { webContents } = win;
    if (webContents.isDestroyed()) return;
    webContents.send(channel, payload);
  } catch (err) {
    if (isDestroyedObjectError(err)) return;
    throw err;
  }
}

/** 2026-08-29 coder(lq): Use electron-updater's network stack for the custom
 * macOS archive download so system proxy/VPN settings are honored consistently
 * with metadata requests. */
function requestMacUpdateWithElectron(
  url: URL,
  redirects = 0,
): Promise<IncomingMessage> {
  if (redirects > 5) {
    return Promise.reject(new Error("Too many redirects while downloading update"));
  }
  return new Promise((resolveRequest, reject) => {
    let settled = false;
    let followingRedirect = false;
    const request = net.request({
      url: url.toString(),
      method: "GET",
      redirect: "manual",
      session: session.fromPartition(ELECTRON_UPDATE_SESSION, { cache: false }),
    });
    const resolveOnce = (response: IncomingMessage) => {
      if (settled) return;
      settled = true;
      clearTimeout(connectionTimeout);
      resolveRequest(response);
    };
    const rejectOnce = (error: Error) => {
      if (settled) return;
      settled = true;
      clearTimeout(connectionTimeout);
      reject(error);
    };
    const connectionTimeout = setTimeout(() => {
      rejectOnce(new Error("Update download timed out"));
      request.abort();
    }, 60 * 1000);
    request.on("response", (response) => {
      clearTimeout(connectionTimeout);
      const status = response.statusCode ?? 0;
      const location = response.headers.location;
      if (status >= 300 && status < 400 && location) {
        followingRedirect = true;
        response.on("data", () => undefined);
        // 2026-08-29 coder(lq): Drain the manual redirect response instead of
        // aborting it; abort emits an error that can race the redirected request.
        void requestMacUpdateWithElectron(
          new URL(Array.isArray(location) ? location[0] : location, url),
          redirects + 1,
        )
          .then(resolveOnce)
          .catch(rejectOnce);
        return;
      }
      if (status < 200 || status >= 300) {
        response.on("data", () => undefined);
        rejectOnce(new Error(`Update download failed with HTTP ${status}`));
        return;
      }
      // 2026-08-29 coder(lq): Electron's response is Node stream-compatible,
      // although Electron does not expose the Node declaration directly.
      resolveOnce(response as unknown as IncomingMessage);
    });
    request.on("error", (error) => {
      if (!followingRedirect) rejectOnce(error);
    });
    request.end();
  });
}

// Single-flight guard around checkForUpdates(). With autoDownload=true the
// startup, periodic, and manual triggers can all kick off downloads, and
// overlapping calls have caused duplicate download warnings in the past
// (see electronjs.org/docs/latest/api/auto-updater). Coalesce concurrent
// callers onto the same in-flight promise.
let inFlightCheck: Promise<unknown> | null = null;
function checkForUpdatesOnce(): Promise<unknown> {
  if (inFlightCheck) return inFlightCheck;
  const p = autoUpdater
    .checkForUpdates()
    .then((result) => {
      // checkForUpdates resolves as soon as metadata is fetched; the actual
      // download (when autoDownload=true) is exposed on result.downloadPromise.
      // Without a handler a download failure becomes an unhandled rejection
      // in the main process — Node may terminate it on future versions.
      void (result as { downloadPromise?: Promise<unknown> } | null)?.downloadPromise?.catch(
        (err) => {
          console.error("Failed to download update:", err);
        },
      );
      return result;
    })
    .finally(() => {
      if (inFlightCheck === p) inFlightCheck = null;
    });
  inFlightCheck = p;
  return p;
}

const PRIVATE_DEPLOYMENT_UPDATE_MESSAGE =
  "Updates are managed by your private Multica deployment administrator.";

export function setupAutoUpdater(
  getMainWindow: () => BrowserWindow | null,
  options: {
    serverUrl?: string;
    /** Kept for backwards compatibility with older callers. */
    enabled?: boolean;
    /** Optional per-build channel, used by the Lechun distribution. */
    channel?: string;
  } = {},
): void {
  // 2026-08-27 coder(lq): Desktop releases are published to the configured
  // public GitHub repository, so private deployments can safely use the same
  // updater without embedding credentials or falling back to upstream.
  const updatesAvailable = options.enabled !== false;
  if (options.channel) {
    // Setting a custom channel also enables allowDowngrade in
    // electron-updater. That side effect is useful when switching between
    // prerelease channels, but not for the official/Lechun distribution
    // split: a newer package must never be replaced by an older one merely
    // because the channel changed.
    autoUpdater.channel = options.channel;
    autoUpdater.allowDowngrade = false;
  }
  const preferencesFilePath = updaterPreferencesPath(app.getPath("userData"));
  const macUpdateCacheDirectory = join(app.getPath("userData"), "updates", "macos");
  let macUpdateInfo: MacUpdateInfo | null = null;
  let downloadedMacUpdate: DownloadedMacUpdate | null = null;
  let macDownloadPromise: Promise<DownloadedMacUpdate> | null = null;
  // 2026-08-29 coder(lq): Coalesce event-driven and manual downloads so two
  // renderer actions cannot overwrite the same verified update archive.
  const downloadMacUpdateOnce = (info: MacUpdateInfo): Promise<DownloadedMacUpdate> => {
    if (macDownloadPromise) return macDownloadPromise;
    const file = selectMacUpdateFile(info, process.arch);
    const resolvedFile = {
      ...file,
      url: resolveMacUpdateUrl(file.url, info.tag),
    };
    sendToLiveRenderer(getMainWindow(), "updater:download-progress", { percent: 0 });
    const promise = prepareMacUpdate(
      resolvedFile,
      macUpdateCacheDirectory,
      info.version,
      process.arch,
      (percent) =>
        sendToLiveRenderer(getMainWindow(), "updater:download-progress", {
          percent,
        }),
      requestMacUpdateWithElectron,
    ).then((update) => {
      update.releaseNotes = info.releaseNotes;
      downloadedMacUpdate = update;
      return update;
    });
    macDownloadPromise = promise;
    void promise
      .finally(() => {
        if (macDownloadPromise === promise) macDownloadPromise = null;
      })
      .catch(() => undefined);
    return promise;
  };
  let automaticUpdatesEnabled =
    updatesAvailable && DEFAULT_UPDATER_PREFERENCES.automaticUpdates;
  let startupCheckElapsed = false;
  let startupTimer: ReturnType<typeof setTimeout> | null = null;
  let periodicTimer: ReturnType<typeof setInterval> | null = null;
  const preferencesReady = loadUpdaterPreferences(preferencesFilePath).then(
    (preferences) => {
      automaticUpdatesEnabled =
        updatesAvailable && preferences.automaticUpdates;
      return preferences;
    },
  );

  const runAutomaticCheck = (errorMessage: string): void => {
    void preferencesReady
      .then(() => {
        if (!updatesAvailable || !automaticUpdatesEnabled) return;
        return checkForUpdatesOnce();
      })
      .catch((err) => {
        console.error(errorMessage, err);
      });
  };

  // Arm the startup + periodic background checks. Idempotent: an already-armed
  // timer is left in place so re-enabling never stacks duplicate schedules.
  const scheduleBackgroundChecks = (): void => {
    if (startupTimer === null && !startupCheckElapsed) {
      // Initial check shortly after startup so we don't block boot.
      startupTimer = setTimeout(() => {
        startupTimer = null;
        startupCheckElapsed = true;
        runAutomaticCheck("Failed to check for updates:");
      }, STARTUP_CHECK_DELAY_MS);
    }
    if (periodicTimer === null) {
      // Background poll so long-running sessions still pick up new releases
      // without requiring the user to restart the app.
      periodicTimer = setInterval(() => {
        runAutomaticCheck("Periodic update check failed:");
      }, PERIODIC_CHECK_INTERVAL_MS);
    }
  };

  // Tear down the scheduled checks outright when automatic updates are turned
  // off. Relying only on an in-callback preference guard leaves the timers
  // running and lets a tick that races the preference flip still fire a check;
  // clearing them makes "disabled" mean no future background work, full stop.
  const cancelBackgroundChecks = (): void => {
    if (startupTimer !== null) {
      clearTimeout(startupTimer);
      startupTimer = null;
    }
    if (periodicTimer !== null) {
      clearInterval(periodicTimer);
      periodicTimer = null;
    }
  };

  autoUpdater.on("update-available", (info) => {
    // Forwarded for renderer-side state tracking only; the notification UI
    // does not render an "available" affordance with autoDownload=true.
    sendToLiveRenderer(getMainWindow(), "updater:update-available", {
      version: info.version,
      releaseNotes: info.releaseNotes,
    });
    if (useMacCustomUpdater) {
      macUpdateInfo = info;
      void (async () => {
        await downloadMacUpdateOnce(info);
        sendToLiveRenderer(getMainWindow(), "updater:update-downloaded", {
          version: info.version,
          releaseNotes: info.releaseNotes,
        });
      })().catch((err) => {
        console.error("Failed to download macOS update:", err);
        sendToLiveRenderer(getMainWindow(), "updater:update-error", {
          message: err instanceof Error ? err.message : String(err),
        });
      });
    }
  });

  autoUpdater.on("update-not-available", () => {
    if (useMacCustomUpdater) {
      macUpdateInfo = null;
      downloadedMacUpdate = null;
    }
  });

  autoUpdater.on("download-progress", (progress) => {
    sendToLiveRenderer(getMainWindow(), "updater:download-progress", {
      percent: progress.percent,
    });
  });

  autoUpdater.on("update-downloaded", (info: UpdateDownloadedEvent) => {
    sendToLiveRenderer(getMainWindow(), "updater:update-downloaded", {
      version: info.version,
      releaseNotes: info.releaseNotes,
    });
  });

  autoUpdater.on("error", (err) => {
    console.error("Auto-updater error:", err);
    sendToLiveRenderer(getMainWindow(), "updater:update-error", {
      message: err instanceof Error ? err.message : String(err),
    });
  });

  // Retained for IPC back-compat with older renderer bundles. On macOS this
  // also provides an explicit retry for the custom download path.
  ipcMain.handle("updater:download", async () => {
    if (!useMacCustomUpdater) return autoUpdater.downloadUpdate();
    if (!macUpdateInfo) throw new Error("No macOS update is available to download");
    const update = await downloadMacUpdateOnce(macUpdateInfo);
    sendToLiveRenderer(getMainWindow(), "updater:update-downloaded", {
      version: macUpdateInfo.version,
      releaseNotes: macUpdateInfo.releaseNotes,
    });
    return [update.archivePath];
  });

  ipcMain.handle("updater:install", async (): Promise<InstallUpdateResult> => {
    try {
      if (useMacCustomUpdater) {
        if (!downloadedMacUpdate) {
          throw new Error("The macOS update has not finished downloading");
        }
        const executablePath = process.execPath;
        const bundleMarker = ".app/";
        const bundleIndex = executablePath.indexOf(bundleMarker);
        if (bundleIndex < 0) throw new Error("Unable to locate the current macOS app bundle");
        const currentAppPath = executablePath.slice(0, bundleIndex + ".app".length);
        await installMacUpdate(
          downloadedMacUpdate,
          currentAppPath,
          process.pid,
          macUpdateCacheDirectory,
        );
        app.quit();
        return { success: true };
      }
      autoUpdater.quitAndInstall(false, true);
      return { success: true };
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      // 2026-08-28 coder(lq): Return installation failures to the renderer so
      // the update prompt does not silently ignore a failed restart.
      console.error("Failed to install update:", err);
      return { success: false, error: message };
    }
  });

  ipcMain.handle(
    "updater:get-preferences",
    async (): Promise<UpdaterPreferences> => {
      await preferencesReady;
      return { automaticUpdates: automaticUpdatesEnabled, updatesAvailable };
    },
  );

  ipcMain.handle(
    "updater:set-automatic-updates",
    async (_event, enabled: unknown): Promise<UpdaterPreferences> => {
      if (typeof enabled !== "boolean") {
        throw new TypeError("automaticUpdates must be a boolean");
      }

      await preferencesReady;
      if (!updatesAvailable && enabled) {
        throw new Error(PRIVATE_DEPLOYMENT_UPDATE_MESSAGE);
      }
      const wasEnabled = automaticUpdatesEnabled;
      const preferences = { automaticUpdates: enabled };
      await saveUpdaterPreferences(preferencesFilePath, preferences);
      automaticUpdatesEnabled = enabled;

      if (!enabled) {
        cancelBackgroundChecks();
      } else if (!wasEnabled) {
        // If the startup check has already passed while the preference was off,
        // enabling it should take effect now instead of waiting up to one hour.
        if (startupCheckElapsed) {
          runAutomaticCheck("Failed to check for updates:");
        }
        scheduleBackgroundChecks();
      }

      return { ...preferences, updatesAvailable };
    },
  );

  ipcMain.handle("updater:check", async (): Promise<ManualUpdateCheckResult> => {
    if (!updatesAvailable) {
      return { ok: false, error: PRIVATE_DEPLOYMENT_UPDATE_MESSAGE };
    }
    try {
      const result = (await checkForUpdatesOnce()) as
        | { updateInfo: { version: string }; isUpdateAvailable?: boolean }
        | null;
      const currentVersion = app.getVersion();
      // Trust electron-updater's own decision rather than re-deriving it from
      // a version-string compare. The two diverge for pre-release channels,
      // staged rollouts, downgrades, and minimum-system-version gates — in
      // those cases updateInfo.version differs from app.getVersion() but no
      // `update-available` event fires, so showing "available" here would
      // promise a download prompt that never appears.
      return {
        ok: true,
        currentVersion,
        latestVersion: result?.updateInfo.version ?? currentVersion,
        available: result?.isUpdateAvailable ?? false,
      };
    } catch (err) {
      return {
        ok: false,
        error: err instanceof Error ? err.message : String(err),
      };
    }
  });

  // Initial check shortly after startup so we don't block boot, plus a
  // background poll for long-running sessions. Both are torn down when the
  // user disables automatic updates and re-armed when they turn them back on.
  if (updatesAvailable) scheduleBackgroundChecks();
}
