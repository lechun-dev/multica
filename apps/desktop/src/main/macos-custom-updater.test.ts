// @vitest-environment node
import { describe, expect, it } from "vitest";
import { resolveMacUpdateUrl, selectMacUpdateFile } from "./macos-custom-updater";

describe("resolveMacUpdateUrl", () => {
  it("resolves GitHub metadata filenames against the private release", () => {
    expect(resolveMacUpdateUrl("multica-lechun-0.4.57-mac-arm64.zip", "v0.4.57")).toBe(
      "https://github.com/lechun-dev/multica/releases/download/v0.4.57/multica-lechun-0.4.57-mac-arm64.zip",
    );
  });

  it("keeps an already-resolved URL unchanged", () => {
    const url = "https://github.com/lechun-dev/multica/releases/download/v0.4.57/update.zip";
    expect(resolveMacUpdateUrl(url, "v0.4.57")).toBe(url);
  });

  it("requires a release tag for relative metadata paths", () => {
    expect(() => resolveMacUpdateUrl("update.zip")).toThrow("release tag");
  });
});

describe("selectMacUpdateFile", () => {
  it("selects the architecture-specific GitHub Release ZIP", () => {
    const file = selectMacUpdateFile(
      {
        version: "0.4.42",
        files: [
          { url: "https://github.com/acme/multica/releases/download/v0.4.42/multica-mac-x64.zip", sha512: "x64" },
          { url: "https://github.com/acme/multica/releases/download/v0.4.42/multica-mac-arm64.zip", sha512: "arm" },
        ],
      },
      "arm64",
    );

    expect(file.url).toContain("mac-arm64.zip");
    expect(file.sha512).toBe("arm");
  });

  it("supports legacy path and checksum fields", () => {
    expect(
      selectMacUpdateFile(
        {
          version: "0.4.42",
          path: "https://example.test/multica.zip",
          sha512: "checksum",
        },
        "arm64",
      ),
    ).toMatchObject({ url: "https://example.test/multica.zip", sha512: "checksum" });
  });

  it("rejects metadata without a macOS ZIP", () => {
    expect(() => selectMacUpdateFile({ version: "0.4.42", files: [] }, "arm64")).toThrow(
      "macOS ZIP",
    );
  });
});
