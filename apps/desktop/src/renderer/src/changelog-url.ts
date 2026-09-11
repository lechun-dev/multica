function releaseAnchor(version: string): string {
  return `release-${version.replace(/\./g, "-")}`;
}

/** Build the public changelog URL for the desktop app's configured web host. */
export function changelogUrl(version?: string): string {
  const runtimeConfig = window.desktopAPI.runtimeConfig;
  const appUrl = runtimeConfig.ok
    ? runtimeConfig.config.appUrl.replace(/\/+$/, "")
    : "https://mission.lechun.cc";

  // 2026-09-11 coder(lq): Keep release copy on the web changelog so desktop
  // entry points never drift from the public version history.
  return `${appUrl}/changelog${version ? `#${releaseAnchor(version)}` : ""}`;
}
