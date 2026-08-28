// @vitest-environment node

import { describe, expect, it } from "vitest";
import { resolveDesktopIdentity } from "./desktop-identity";

describe("resolveDesktopIdentity", () => {
  it("keeps local development isolated", () => {
    expect(resolveDesktopIdentity({ isDev: true })).toEqual({
      variant: "official",
      productName: "Multica Canary",
      appId: "ai.multica.desktop.dev",
      protocol: "multica-dev",
      oauthClient: "desktop-dev",
    });
  });

  it("uses the custom protocol and OAuth client for the Lechun build", () => {
    expect(
      resolveDesktopIdentity({ isDev: false, variant: "lechun" }),
    ).toEqual({
      variant: "lechun",
      productName: "Multica Lechun",
      appId: "ai.multica.desktop.lechun",
      protocol: "multica-lechun",
      oauthClient: "desktop-lechun",
    });
  });

  it("falls back to the official production identity", () => {
    expect(resolveDesktopIdentity({ isDev: false })).toEqual({
      variant: "official",
      productName: "Multica",
      appId: "ai.multica.desktop",
      protocol: "multica",
      oauthClient: "desktop",
    });
  });
});
