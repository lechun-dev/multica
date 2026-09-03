// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  resolveDesktopIdentity,
  resolveDesktopUpdateChannel,
} from "./desktop-identity";

describe("resolveDesktopIdentity", () => {
  it("keeps local development isolated", () => {
    expect(resolveDesktopIdentity({ isDev: true })).toEqual({
      variant: "official",
      productName: "MissionOS Canary",
      userDataDirectoryName: "Multica Canary",
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
      productName: "MissionOS",
      userDataDirectoryName: "Multica Lechun",
      appId: "ai.multica.desktop.lechun",
      protocol: "multica-lechun",
      oauthClient: "desktop-lechun",
    });
  });

  it("falls back to the official production identity", () => {
    expect(resolveDesktopIdentity({ isDev: false })).toEqual({
      variant: "official",
      productName: "MissionOS",
      userDataDirectoryName: "Multica",
      appId: "ai.multica.desktop",
      protocol: "multica",
      oauthClient: "desktop",
    });
  });

  it("isolates the Lechun preview identity", () => {
    expect(
      resolveDesktopIdentity({ isDev: false, variant: "lechun-preview" }),
    ).toEqual({
      variant: "lechun-preview",
      productName: "MissionOS Preview",
      userDataDirectoryName: "Multica Lechun Preview",
      appId: "ai.multica.desktop.lechun.preview",
      protocol: "multica-lechun-preview",
      oauthClient: "desktop-lechun-preview",
    });
  });
});

describe("resolveDesktopUpdateChannel", () => {
  it("keeps the official build on electron-builder's default channel", () => {
    expect(
      resolveDesktopUpdateChannel({
        variant: "official",
        platform: "darwin",
        arch: "x64",
      }),
    ).toBeNull();
  });

  it("namespaces the Lechun update channel", () => {
    expect(
      resolveDesktopUpdateChannel({
        variant: "lechun",
        platform: "win32",
        arch: "x64",
      }),
    ).toBe("latest-lechun");
  });

  it("keeps Lechun architecture metadata from colliding", () => {
    expect(
      resolveDesktopUpdateChannel({
        variant: "lechun",
        platform: "darwin",
        arch: "x64",
      }),
    ).toBe("latest-lechun-x64");
    expect(
      resolveDesktopUpdateChannel({
        variant: "lechun",
        platform: "win32",
        arch: "arm64",
      }),
    ).toBe("latest-lechun-arm64");
  });

  it("uses a separate preview update feed", () => {
    expect(
      resolveDesktopUpdateChannel({
        variant: "lechun-preview",
        platform: "darwin",
        arch: "arm64",
      }),
    ).toBe("latest-lechun-preview");
  });
});
