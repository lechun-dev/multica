import { execFile } from "child_process";
import { readdir } from "fs/promises";
import { join } from "path";

const VERIFY_TIMEOUT_MS = 5_000;

export interface CodexDiscoveryOptions {
  home: string;
  env?: NodeJS.ProcessEnv;
  platform?: NodeJS.Platform;
}

export type CodexVerifier = (candidate: string) => Promise<boolean>;

export function isAgentCliMissingError(...parts: unknown[]): boolean {
  return parts.some(
    (part) =>
      typeof part === "string" &&
      part.toLowerCase().includes("no agent cli found"),
  );
}

function uniquePaths(paths: Array<string | undefined>): string[] {
  const seen = new Set<string>();
  const result: string[] = [];
  for (const path of paths) {
    const normalized = path?.trim();
    if (!normalized || seen.has(normalized)) continue;
    seen.add(normalized);
    result.push(normalized);
  }
  return result;
}

export function codexCandidatesForSelection(selectedPath: string): string[] {
  if (selectedPath.toLowerCase().endsWith(".app")) {
    return [join(selectedPath, "Contents", "Resources", "codex")];
  }
  return [selectedPath];
}

async function versionManagerCandidates(home: string): Promise<string[]> {
  const roots = [
    join(home, ".nvm", "versions", "node"),
    join(home, ".fnm", "node-versions"),
  ];
  const candidates: string[] = [];

  for (const root of roots) {
    try {
      const entries = await readdir(root, { withFileTypes: true });
      for (const entry of entries) {
        if (!entry.isDirectory()) continue;
        candidates.push(
          root.includes(".fnm")
            ? join(root, entry.name, "installation", "bin", "codex")
            : join(root, entry.name, "bin", "codex"),
        );
      }
    } catch {
      // 2026-09-16 coder(lq): Missing version-manager directories are normal.
    }
  }

  return candidates;
}

export async function codexDiscoveryCandidates({
  home,
  env = process.env,
  platform = process.platform,
}: CodexDiscoveryOptions): Promise<string[]> {
  const pathCandidates = (env.PATH ?? "")
    .split(platform === "win32" ? ";" : ":")
    .filter(Boolean)
    .map((directory) =>
      join(directory, platform === "win32" ? "codex.exe" : "codex"),
    );
  const managedCandidates = await versionManagerCandidates(home);

  return uniquePaths([
    ...pathCandidates,
    ...(platform === "darwin"
      ? [
          "/Applications/ChatGPT.app/Contents/Resources/codex",
          "/Applications/Codex.app/Contents/Resources/codex",
          join(
            home,
            "Applications",
            "ChatGPT.app",
            "Contents",
            "Resources",
            "codex",
          ),
          join(
            home,
            "Applications",
            "Codex.app",
            "Contents",
            "Resources",
            "codex",
          ),
        ]
      : []),
    join(home, ".volta", "bin", platform === "win32" ? "codex.exe" : "codex"),
    join(home, ".local", "bin", platform === "win32" ? "codex.exe" : "codex"),
    join(home, "Library", "pnpm", "codex"),
    ...managedCandidates,
  ]);
}

export async function verifyCodexCli(candidate: string): Promise<boolean> {
  return new Promise((resolve) => {
    execFile(
      candidate,
      ["--version"],
      { timeout: VERIFY_TIMEOUT_MS, windowsHide: true },
      (error) => resolve(error === null),
    );
  });
}

export async function findUsableCodexCli(
  candidates: string[],
  verify: CodexVerifier = verifyCodexCli,
): Promise<string | null> {
  for (const candidate of uniquePaths(candidates)) {
    if (await verify(candidate)) return candidate;
  }
  return null;
}
