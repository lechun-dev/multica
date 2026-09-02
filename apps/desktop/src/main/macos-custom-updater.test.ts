// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { createHash } from "node:crypto";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import type { IncomingMessage } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Readable } from "node:stream";
import {
  downloadMacUpdate,
  resolveMacUpdateUrl,
  selectMacUpdateFile,
} from "./macos-custom-updater";

const temporaryDirectories: string[] = [];

function createTemporaryDirectory(): string {
  const directory = mkdtempSync(join(tmpdir(), "multica-mac-update-test-"));
  temporaryDirectories.push(directory);
  return directory;
}

function createResponse(body: Buffer): IncomingMessage {
  const response = Readable.from([body]) as unknown as IncomingMessage;
  response.headers = { "content-length": String(body.length) };
  return response;
}

function createInterruptedResponse(body: Buffer): IncomingMessage {
  const response = new Readable({
    read() {
      this.push(body);
      this.destroy(new Error("connection interrupted"));
    },
  }) as unknown as IncomingMessage;
  response.headers = { "content-length": String(body.length * 2) };
  return response;
}

function sha512(body: Buffer): string {
  return createHash("sha512").update(body).digest("base64");
}

afterEach(() => {
  vi.useRealTimers();
  for (const directory of temporaryDirectories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

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

describe("downloadMacUpdate", () => {
  it("downloads the archive, verifies it, and reports completion", async () => {
    const directory = createTemporaryDirectory();
    const destination = join(directory, "update.zip");
    const body = Buffer.from("verified update archive");
    const progress: number[] = [];

    await downloadMacUpdate(
      { url: "https://example.test/update.zip", sha512: sha512(body), size: body.length },
      destination,
      (percent) => progress.push(percent),
      async () => createResponse(body),
    );

    expect(readFileSync(destination)).toEqual(body);
    expect(progress.at(-1)).toBe(100);
    expect(existsSync(`${destination}.download`)).toBe(false);
  });

  it("retries a failed request and replaces the temporary download", async () => {
    const directory = createTemporaryDirectory();
    const destination = join(directory, "update.zip");
    const body = Buffer.from("archive after retry");
    const requester = vi
      .fn()
      .mockRejectedValueOnce(new Error("temporary network failure"))
      .mockResolvedValueOnce(createResponse(body));
    const progress: number[] = [];

    const download = downloadMacUpdate(
      { url: "https://example.test/update.zip", sha512: sha512(body), size: body.length },
      destination,
      (percent) => progress.push(percent),
      requester,
      async () => undefined,
    );
    await download;

    expect(requester).toHaveBeenCalledTimes(2);
    expect(readFileSync(destination)).toEqual(body);
    expect(progress).toContain(0);
    expect(progress.at(-1)).toBe(100);
  });

  it("fails after three interrupted attempts and removes partial files", async () => {
    const directory = createTemporaryDirectory();
    const destination = join(directory, "update.zip");
    const requester = vi.fn(async () => createInterruptedResponse(Buffer.from("partial")));

    const download = downloadMacUpdate(
      {
        url: "https://example.test/update.zip",
        sha512: sha512(Buffer.from("complete archive")),
      },
      destination,
      undefined,
      requester,
      async () => undefined,
    );
    await expect(download).rejects.toThrow("connection interrupted");

    expect(requester).toHaveBeenCalledTimes(3);
    expect(existsSync(destination)).toBe(false);
    expect(existsSync(`${destination}.download`)).toBe(false);
  });
});
