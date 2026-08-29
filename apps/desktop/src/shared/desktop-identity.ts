/**
 * Identity values that must stay in sync across the Electron main process,
 * renderer and the OAuth hand-off page.
 *
 * The custom build is selected at build time. Keeping this as a pure helper
 * means the official Multica build and local development retain their
 * existing behavior without reading runtime environment variables.
 */
export type DesktopVariant = "official" | "lechun";

export type DesktopIdentity = {
  variant: DesktopVariant;
  productName: string;
  /** Directory name under Electron's appData root. */
  userDataDirectoryName: string;
  appId: string;
  protocol: "multica" | "multica-dev" | "multica-lechun";
  oauthClient: "desktop" | "desktop-dev" | "desktop-lechun";
};

export const LECHUN_UPDATE_CHANNEL = "latest-lechun" as const;

type ResolveDesktopIdentityOptions = {
  isDev: boolean;
  variant?: string;
};

/** Resolve the app identity for one of the three supported desktop modes. */
export function resolveDesktopIdentity({
  isDev,
  variant,
}: ResolveDesktopIdentityOptions): DesktopIdentity {
  if (isDev) {
    return {
      variant: "official",
      productName: "Multica Canary",
      userDataDirectoryName: "Multica Canary",
      appId: "ai.multica.desktop.dev",
      protocol: "multica-dev",
      oauthClient: "desktop-dev",
    };
  }

  if (variant === "lechun") {
    return {
      variant: "lechun",
      productName: "Mission",
      userDataDirectoryName: "Multica Lechun",
      appId: "ai.multica.desktop.lechun",
      protocol: "multica-lechun",
      oauthClient: "desktop-lechun",
    };
  }

  return {
    variant: "official",
    productName: "Multica",
    userDataDirectoryName: "Multica",
    appId: "ai.multica.desktop",
    protocol: "multica",
    oauthClient: "desktop",
  };
}

/**
 * Resolve the update metadata channel for a packaged desktop identity.
 *
 * The Lechun build uses a namespaced channel so it never consumes the
 * official build's metadata. macOS x64 and Windows arm64 need an additional
 * suffix because electron-builder otherwise gives those architectures the
 * same metadata filename as the default architecture.
 */
export function resolveDesktopUpdateChannel({
  variant,
  platform,
  arch,
}: {
  variant: DesktopVariant;
  platform: string;
  arch: string;
}): string | null {
  if (variant !== "lechun") return null;

  if (platform === "darwin" && arch === "x64") {
    return `${LECHUN_UPDATE_CHANNEL}-x64`;
  }
  if (platform === "win32" && arch === "arm64") {
    return `${LECHUN_UPDATE_CHANNEL}-arm64`;
  }

  return LECHUN_UPDATE_CHANNEL;
}
