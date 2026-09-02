// @vitest-environment node
import { describe, expect, it } from "vitest";
import { hasCompleteAssetSet, parseReleaseAssets } from "./parse-release-assets";

function asset(name: string) {
  return {
    name,
    browser_download_url: `https://github.test/releases/${name}`,
  };
}

describe("parseReleaseAssets", () => {
  it("keeps both Apple Silicon and Intel macOS installers", () => {
    const assets = parseReleaseAssets([
      asset("multica-lechun-0.4.2-mac-arm64.dmg"),
      asset("multica-lechun-0.4.2-mac-arm64.zip"),
      asset("multica-lechun-0.4.2-mac-x64.dmg"),
      asset("multica-lechun-0.4.2-mac-x64.zip"),
      asset("multica-lechun-0.4.2-mac-x64.dmg.blockmap"),
      asset("latest-x64-mac.yml"),
    ]);

    expect(assets).toEqual({
      macArm64Dmg:
        "https://github.test/releases/multica-lechun-0.4.2-mac-arm64.dmg",
      macArm64Zip:
        "https://github.test/releases/multica-lechun-0.4.2-mac-arm64.zip",
      macX64Dmg:
        "https://github.test/releases/multica-lechun-0.4.2-mac-x64.dmg",
      macX64Zip:
        "https://github.test/releases/multica-lechun-0.4.2-mac-x64.zip",
    });
  });
});

/** Every artifact name a finished release publishes, in real-world form —
 *  note Linux arch varies by format (x86_64 for AppImage/rpm, amd64 for
 *  deb; aarch64 for rpm, arm64 for the rest). */
const ALL_ARTIFACT_NAMES = [
  "multica-lechun-0.4.27-mac-arm64.dmg",
  "multica-lechun-0.4.27-mac-arm64.zip",
  "multica-lechun-0.4.27-mac-x64.dmg",
  "multica-lechun-0.4.27-mac-x64.zip",
  "multica-lechun-0.4.27-windows-x64.exe",
  "multica-lechun-0.4.27-linux-x86_64.AppImage",
  "multica-lechun-0.4.27-linux-amd64.deb",
  "multica-lechun-0.4.27-linux-x86_64.rpm",
  "multica-lechun-0.4.27-linux-arm64.AppImage",
  "multica-lechun-0.4.27-linux-arm64.deb",
  "multica-lechun-0.4.27-linux-aarch64.rpm",
];

describe("hasCompleteAssetSet", () => {
  it("accepts a release carrying the supported Mac and Windows artifacts", () => {
    const assets = parseReleaseAssets(ALL_ARTIFACT_NAMES.map(asset));
    expect(hasCompleteAssetSet(assets)).toBe(true);
  });

  it("does not require retired Linux artifacts", () => {
    const assets = parseReleaseAssets(
      ALL_ARTIFACT_NAMES.filter((n) => !n.includes("-linux-")).map(asset),
    );
    expect(hasCompleteAssetSet(assets)).toBe(true);
  });

  it("rejects a release missing any supported artifact", () => {
    const supportedArtifacts = ALL_ARTIFACT_NAMES.filter(
      (n) => !n.includes("-linux-"),
    );
    for (const dropped of supportedArtifacts) {
      const assets = parseReleaseAssets(
        ALL_ARTIFACT_NAMES.filter((n) => n !== dropped).map(asset),
      );
      expect(hasCompleteAssetSet(assets), `missing ${dropped}`).toBe(false);
    }
  });

  it("rejects an empty asset set", () => {
    expect(hasCompleteAssetSet({})).toBe(false);
  });

  it("ignores official Multica artifacts", () => {
    const assets = parseReleaseAssets([
      asset("multica-desktop-0.4.45-mac-arm64.dmg"),
      asset("multica-desktop-0.4.45-windows-x64.exe"),
    ]);
    expect(assets).toEqual({});
  });
});
