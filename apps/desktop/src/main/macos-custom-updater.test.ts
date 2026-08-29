// @vitest-environment node
import { describe, expect, it } from "vitest";
import { selectMacUpdateFile } from "./macos-custom-updater";

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
