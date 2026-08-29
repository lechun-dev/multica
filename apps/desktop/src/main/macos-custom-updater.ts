import { createHash } from "node:crypto";
import { createWriteStream, promises as fs } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { spawn } from "node:child_process";
import { get as httpsGet } from "node:https";
import { get as httpGet } from "node:http";

export interface MacUpdateFile {
  url?: string;
  path?: string;
  sha512?: string;
  size?: number;
}

export interface MacUpdateInfo {
  version: string;
  /** 2026-08-29 coder(lq): GitHubProvider adds this runtime-only release tag. */
  tag?: string;
  files?: MacUpdateFile[];
  path?: string;
  sha512?: string;
  releaseNotes?: unknown;
}

const PRIVATE_RELEASE_DOWNLOAD_BASE =
  "https://github.com/lechun-dev/multica/releases/download";

/** 2026-08-29 coder(lq): Resolve GitHub metadata filenames for private builds.
 *
 * Resolve the file URL from electron-updater's GitHub metadata.
 *
 * GitHubProvider normally resolves `files[].url` before downloading, but the
 * macOS ad-hoc-signing path intentionally downloads the selected file itself.
 * Release metadata commonly contains only a filename, so resolve that name
 * against the private release tag here instead of passing it to `new URL()`.
 */
export function resolveMacUpdateUrl(value: string, releaseTag?: string): string {
  if (/^https?:\/\//i.test(value)) return value;
  if (!releaseTag) {
    throw new Error("GitHub Release metadata does not contain a release tag");
  }
  const fileName = value.replace(/^\/+/, "");
  return new URL(
    fileName,
    `${PRIVATE_RELEASE_DOWNLOAD_BASE}/${encodeURIComponent(releaseTag)}/`,
  ).toString();
}

const UPDATE_DOWNLOAD_TIMEOUT_MS = 5 * 60 * 1000;

export interface DownloadedMacUpdate {
  version: string;
  appPath: string;
  archivePath: string;
  releaseNotes?: unknown;
}

export function selectMacUpdateFile(
  info: MacUpdateInfo,
  arch: string,
): MacUpdateFile & { url: string; sha512: string } {
  const files = info.files ?? [];
  const candidates = files.filter((file) => {
    const value = file.url ?? file.path ?? "";
    return value.toLowerCase().endsWith(".zip");
  });
  const preferred = candidates.find((file) =>
    (file.url ?? file.path ?? "").toLowerCase().includes(`-${arch.toLowerCase()}.zip`),
  );
  const file = preferred ?? candidates[0];
  if (!file && !info.path) {
    throw new Error("GitHub Release metadata does not contain a macOS ZIP");
  }
  const url = file?.url ?? file?.path ?? info.path;
  const sha512 = file?.sha512 ?? info.sha512;
  if (!url || !sha512) {
    throw new Error("GitHub Release metadata does not contain a macOS ZIP and checksum");
  }
  return { ...file, url, sha512 };
}

function request(url: URL, redirects = 0): Promise<import("node:http").IncomingMessage> {
  if (redirects > 5) return Promise.reject(new Error("Too many redirects while downloading update"));
  const get = url.protocol === "http:" ? httpGet : httpsGet;
  return new Promise((resolveRequest, reject) => {
    const req = get(url, (response) => {
      response.setTimeout(UPDATE_DOWNLOAD_TIMEOUT_MS, () => {
        response.destroy(new Error("Update download timed out"));
      });
      const status = response.statusCode ?? 0;
      if (status >= 300 && status < 400 && response.headers.location) {
        response.resume();
        void request(new URL(response.headers.location, url), redirects + 1)
          .then(resolveRequest)
          .catch(reject);
        return;
      }
      if (status < 200 || status >= 300) {
        response.resume();
        reject(new Error(`Update download failed with HTTP ${status}`));
        return;
      }
      resolveRequest(response);
    });
    req.setTimeout(UPDATE_DOWNLOAD_TIMEOUT_MS, () => {
      req.destroy(new Error("Update download timed out"));
    });
    req.on("error", reject);
  });
}

export async function downloadMacUpdate(
  file: { url: string; sha512: string; size?: number },
  destination: string,
  onProgress?: (percent: number) => void,
): Promise<void> {
  await fs.mkdir(dirname(destination), { recursive: true });
  const temporary = `${destination}.download`;
  await fs.rm(temporary, { force: true });
  const response = await request(new URL(file.url));
  const total = Number(response.headers["content-length"] ?? file.size ?? 0);
  const hash = createHash("sha512");
  let received = 0;
  try {
    await new Promise<void>((resolveDownload, reject) => {
      const output = createWriteStream(temporary);
      response.on("data", (chunk: Buffer) => {
        received += chunk.length;
        hash.update(chunk);
        if (total > 0) onProgress?.((received / total) * 100);
      });
      response.on("error", reject);
      output.on("error", reject);
      output.on("finish", resolveDownload);
      response.pipe(output);
    });
    const actual = hash.digest("base64");
    if (actual !== file.sha512) {
      throw new Error("Downloaded update checksum does not match GitHub Release metadata");
    }
    await fs.rename(temporary, destination);
  } catch (error) {
    await fs.rm(temporary, { force: true });
    throw error;
  }
}

async function run(command: string, args: string[]): Promise<void> {
  await new Promise<void>((resolveRun, reject) => {
    const child = spawn(command, args, { stdio: "ignore" });
    child.once("error", reject);
    child.once("exit", (code) =>
      code === 0 ? resolveRun() : reject(new Error(`${command} exited with code ${code}`)),
    );
  });
}

async function findAppBundle(root: string): Promise<string> {
  const found: string[] = [];
  async function visit(directory: string): Promise<void> {
    for (const entry of await fs.readdir(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name);
      if (entry.isDirectory() && entry.name.endsWith(".app")) found.push(path);
      else if (entry.isDirectory()) await visit(path);
    }
  }
  await visit(root);
  if (found.length !== 1) throw new Error("Update archive must contain exactly one macOS app");
  return found[0];
}

export async function prepareMacUpdate(
  file: { url: string; sha512: string; size?: number },
  cacheDirectory: string,
  version: string,
  arch: string,
  onProgress?: (percent: number) => void,
): Promise<DownloadedMacUpdate> {
  const archivePath = join(cacheDirectory, `multica-${version}-${arch}.zip`);
  await downloadMacUpdate(file, archivePath, onProgress);
  const extractionDirectory = join(cacheDirectory, `extract-${version}-${arch}`);
  await fs.rm(extractionDirectory, { recursive: true, force: true });
  await fs.mkdir(extractionDirectory, { recursive: true });
  await run("ditto", ["-x", "-k", archivePath, extractionDirectory]);
  const appPath = resolve(await findAppBundle(extractionDirectory));
  return { version, appPath, archivePath };
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", "'\\''")}'`;
}

export async function installMacUpdate(
  update: DownloadedMacUpdate,
  currentAppPath: string,
  currentPid: number,
  cacheDirectory: string,
): Promise<void> {
  const scriptPath = join(cacheDirectory, `install-${update.version}-${Date.now()}.sh`);
  const backupPath = `${currentAppPath}.backup-${Date.now()}`;
  const script = `#!/bin/sh
set -eu
while kill -0 ${currentPid} 2>/dev/null; do sleep 1; done
backup=${shellQuote(backupPath)}
old=${shellQuote(currentAppPath)}
new=${shellQuote(update.appPath)}
if [ -e "$old" ]; then mv "$old" "$backup"; fi
if ! mv "$new" "$old"; then
  if [ -e "$backup" ]; then mv "$backup" "$old"; fi
  exit 1
fi
rm -rf "$backup"
open "$old"
rm -f ${shellQuote(scriptPath)}
`;
  await fs.writeFile(scriptPath, script, { mode: 0o700 });
  const child = spawn("/bin/sh", [scriptPath], {
    detached: true,
    stdio: "ignore",
  });
  child.unref();
}
