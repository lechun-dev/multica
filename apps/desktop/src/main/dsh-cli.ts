import { spawn } from "node:child_process";
import { stat } from "node:fs/promises";
import { homedir } from "node:os";
import { posix, win32 } from "node:path";
import type { DshCliStatus } from "../shared/dsh-cli";

type Probe = (file: string, env: NodeJS.ProcessEnv, timeoutMs: number) => Promise<void>;
class DshProbeError extends Error {
  constructor(readonly reason: DshCliStatus["reason"]) { super(reason); }
}

export function dshDesktopCandidates(platform: NodeJS.Platform, home: string, env: NodeJS.ProcessEnv): string[] {
  if (platform === "darwin") return [
    "/Applications/DeepSeek Harness.app/Contents/Resources",
    posix.join(home, "Applications/DeepSeek Harness.app/Contents/Resources"),
  ];
  if (platform === "win32") {
    const local = env.LOCALAPPDATA ?? win32.join(home, "AppData", "Local");
    return [...new Set([
      win32.join(local, "Programs", "DeepSeek Harness", "resources"),
      ...[env.ProgramFiles, env["ProgramFiles(x86)"]].filter((p): p is string => Boolean(p))
        .map((p) => win32.join(p, "DeepSeek Harness", "resources")),
    ])];
  }
  return [];
}

// 2026-10-08 coder(lq): Initialize is read-only: no persistent session, model request, plugin install or global PATH mutation.
export const probeDshACP: Probe = (file, env, timeoutMs) => new Promise((resolve, reject) => {
  const windows = process.platform === "win32";
  const command = windows && /\.(cmd|bat)$/iu.test(file)
    ? win32.join(env.SystemRoot ?? env.WINDIR ?? "C:\\Windows", "System32", "WindowsPowerShell", "v1.0", "powershell.exe") : file;
  const args = command === file ? ["--profile", "acp"]
    : ["-NoProfile", "-NonInteractive", "-Command", "& $env:MISSIONOS_DSH_LAUNCHER --profile acp; exit $LASTEXITCODE"];
  const child = spawn(command, args, {
    env: { ...env, MISSIONOS_DSH_LAUNCHER: file, DSH_TELEMETRY_DISABLED: "1" },
    windowsHide: true, detached: !windows, stdio: ["pipe", "pipe", "ignore"],
  });
  let settled = false;
  let buffer = "";
  const finish = (error?: DshProbeError) => {
    if (settled) return;
    settled = true;
    clearTimeout(timer);
    child.stdin.end();
    // 2026-10-08 coder(lq): Closing stdin alone cannot reap a broken CLI or a child holding inherited pipes.
    if (child.pid) {
      if (windows) {
        const killer = spawn(win32.join(env.SystemRoot ?? "C:\\Windows", "System32", "taskkill.exe"), ["/pid", String(child.pid), "/t", "/f"], { windowsHide: true, stdio: "ignore" });
        killer.on("error", () => child.kill());
      } else {
        try { process.kill(-child.pid, "SIGKILL"); } catch { child.kill(); }
      }
    }
    child.stdout.destroy();
    if (error) reject(error); else resolve();
  };
  const timer = setTimeout(() => finish(new DshProbeError("probe_timeout")), timeoutMs);
  child.on("error", () => finish(new DshProbeError("launch_failed")));
  child.stdin.on("error", () => finish(new DshProbeError("launch_failed")));
  child.on("close", () => finish(new DshProbeError("probe_failed")));
  child.stdout.setEncoding("utf8");
  child.stdout.on("data", (chunk: string) => {
    buffer += chunk;
    if (buffer.length > 65536) { finish(new DshProbeError("protocol_incompatible")); return; }
    let newline: number;
    while ((newline = buffer.indexOf("\n")) >= 0) {
      const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
      let frame: unknown;
      try { frame = JSON.parse(line); } catch { continue; }
      if (!frame || typeof frame !== "object" || !("id" in frame) || frame.id !== 1) continue;
      if ("error" in frame && frame.error) { finish(new DshProbeError("probe_failed")); return; }
      if (!("jsonrpc" in frame) || frame.jsonrpc !== "2.0" || !("result" in frame)
        || !frame.result || typeof frame.result !== "object" || !("protocolVersion" in frame.result)
        || frame.result.protocolVersion !== 1) { finish(new DshProbeError("protocol_incompatible")); return; }
      finish(); return;
    }
  });
  child.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: {
    protocolVersion: 1, clientInfo: { name: "missionos", version: "1" }, clientCapabilities: {},
  } })}\n`);
});

export class DshCliManager {
  private operation: Promise<DshCliStatus> | undefined;
  private readonly platform: NodeJS.Platform;
  private readonly env: NodeJS.ProcessEnv;
  private readonly home: string;
  private readonly probe: Probe;
  private readonly isFile: (path: string) => Promise<boolean>;
  private readonly explicitPath: string | undefined;
  private selectedPath: string | undefined;
  private readonly timeoutMs: number;

  constructor(options: {
    platform?: NodeJS.Platform; env?: NodeJS.ProcessEnv; home?: string;
    probe?: Probe; isFile?: (path: string) => Promise<boolean>; timeoutMs?: number;
  } = {}) {
    this.platform = options.platform ?? process.platform;
    this.env = options.env ?? process.env;
    this.home = options.home ?? homedir();
    this.explicitPath = this.env.MULTICA_DSH_PATH?.trim() || undefined;
    this.probe = options.probe ?? probeDshACP;
    this.timeoutMs = options.timeoutMs ?? 4_000;
    this.isFile = options.isFile ?? (async (path) => {
      try { return (await stat(path)).isFile(); }
      catch (error) {
        if (["ENOENT", "ENOTDIR"].includes((error as NodeJS.ErrnoException).code ?? "")) return false;
        throw error;
      }
    });
  }

  inspect(): Promise<DshCliStatus> {
    return this.operation ??= this.check().finally(() => { this.operation = undefined; });
  }
  repair(): Promise<DshCliStatus> { return this.inspect(); }

  private async candidates(): Promise<string[]> {
    if (this.explicitPath) return [this.explicitPath];
    const path = this.platform === "win32" ? win32 : posix;
    const names = this.platform === "win32" ? ["dsh.exe", "dsh.cmd", "dsh.bat"] : ["dsh"];
    const candidates: string[] = [];
    search: for (const dir of (this.env.PATH ?? "").split(this.platform === "win32" ? ";" : ":").filter(Boolean)) {
      for (const name of names) {
        const candidate = path.join(dir, name);
        if (await this.isFile(candidate)) { candidates.push(candidate); break search; }
      }
    }
    for (const resources of dshDesktopCandidates(this.platform, this.home, this.env)) {
      const candidate = path.join(resources, "runtime", "cli", "bin", this.platform === "win32" ? "dsh.cmd" : "dsh");
      if (await this.isFile(candidate)) { candidates.push(candidate); break; }
    }
    return [...new Set(candidates)];
  }

  private async check(): Promise<DshCliStatus> {
    // 2026-10-08 coder(lq): Invalidate only our own last selection. An operator override is never silently repaired or replaced.
    if (!this.explicitPath && this.selectedPath && this.env.MULTICA_DSH_PATH === this.selectedPath) delete this.env.MULTICA_DSH_PATH;
    this.selectedPath = undefined;
    try {
      const candidates = await this.candidates();
      if (candidates.length === 0) return { state: "not_installed" };
      let reason: DshCliStatus["reason"] = "probe_failed";
      for (const candidate of candidates) {
        try {
          await this.probe(candidate, this.env, this.timeoutMs);
          this.selectedPath = candidate;
          this.env.MULTICA_DSH_PATH = candidate;
          return { state: "ready" };
        } catch (error) { reason = error instanceof DshProbeError ? error.reason : "probe_failed"; }
      }
      return { state: "error", reason };
    } catch { return { state: "error" }; }
  }
}
