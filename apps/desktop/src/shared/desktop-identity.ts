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
  appId: string;
  protocol: "multica" | "multica-dev" | "multica-lechun";
  oauthClient: "desktop" | "desktop-dev" | "desktop-lechun";
};

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
      appId: "ai.multica.desktop.dev",
      protocol: "multica-dev",
      oauthClient: "desktop-dev",
    };
  }

  if (variant === "lechun") {
    return {
      variant: "lechun",
      productName: "Multica Lechun",
      appId: "ai.multica.desktop.lechun",
      protocol: "multica-lechun",
      oauthClient: "desktop-lechun",
    };
  }

  return {
    variant: "official",
    productName: "Multica",
    appId: "ai.multica.desktop",
    protocol: "multica",
    oauthClient: "desktop",
  };
}
