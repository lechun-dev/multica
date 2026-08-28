export interface UpdaterPreferences {
  automaticUpdates: boolean;
  /** Optional for compatibility with older renderer/main-process bundles. */
  updatesAvailable?: boolean;
}

export type InstallUpdateResult =
  | { success: true }
  | { success: false; error: string };

export type ManualUpdateCheckResult =
  | {
      ok: true;
      currentVersion: string;
      latestVersion: string;
      available: boolean;
    }
  | { ok: false; error: string };
