import { execFile } from "node:child_process";
import { stat } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, posix, win32 } from "node:path";
import { promisify } from "node:util";
import type { DshCliStatus } from "../shared/dsh-cli";
import { appendMissingPathDirs } from "./path-fallback";

const exec = promisify(execFile);
type Run = (file: string, args: string[], env: NodeJS.ProcessEnv) => Promise<string>;
interface Installation {
  node: string;
  worker: string;
  launcher: string;
}
interface CommandState {
  fingerprint: string;
  managed: boolean;
  available: boolean;
  occupied: boolean;
  shadowed: boolean;
}

export function dshDesktopCandidates(
  platform: NodeJS.Platform,
  home: string,
  env: NodeJS.ProcessEnv,
): string[] {
  if (platform === "darwin") {
    return [
      "/Applications/DeepSeek Harness.app/Contents/Resources",
      posix.join(home, "Applications/DeepSeek Harness.app/Contents/Resources"),
    ];
  }
  if (platform === "win32") {
    return [...new Set([
      env.LOCALAPPDATA ?? win32.join(home, "AppData", "Local"),
      env.ProgramFiles,
      env["ProgramFiles(x86)"],
    ].filter((root): root is string => Boolean(root)).map((root, index) =>
      win32.join(root, ...(index === 0 ? ["Programs"] : []), "DeepSeek Harness", "resources"),
    ))];
  }
  return [];
}

// 2026-10-08 coder(lq): Registration uses only the installed DSH worker; existing commands are probed without replacing them.
export class DshCliManager {
  private operation: Promise<DshCliStatus> | undefined;
  private readonly platform: NodeJS.Platform;
  private readonly env: NodeJS.ProcessEnv;
  private readonly home: string;
  private readonly run: Run;
  private readonly isFile: (path: string) => Promise<boolean>;

  constructor(options: {
    platform?: NodeJS.Platform;
    env?: NodeJS.ProcessEnv;
    home?: string;
    run?: Run;
    isFile?: (path: string) => Promise<boolean>;
  } = {}) {
    this.platform = options.platform ?? process.platform;
    this.env = options.env ?? process.env;
    this.home = options.home ?? homedir();
    this.run = options.run ?? (async (file, args, env) =>
      (await exec(file, args, { env, timeout: 60_000, maxBuffer: 65536, windowsHide: true })).stdout);
    this.isFile = options.isFile ?? (async (path) => {
      try { return (await stat(path)).isFile(); }
      catch (error) {
        if (["ENOENT", "ENOTDIR"].includes((error as NodeJS.ErrnoException).code ?? "")) return false;
        throw error;
      }
    });
  }

  inspect(): Promise<DshCliStatus> {
    return this.operation ?? this.check(false);
  }

  repair(): Promise<DshCliStatus> {
    return this.operation ??= this.check(true).finally(() => { this.operation = undefined; });
  }

  private async locate(): Promise<Installation | null> {
    const path = this.platform === "win32" ? win32 : posix;
    for (const resources of dshDesktopCandidates(this.platform, this.home, this.env)) {
      const executable = this.platform === "win32"
        ? path.join(resources, "..", "DeepSeek Harness.exe")
        : path.join(resources, "..", "MacOS", "DeepSeek Harness");
      if (!await this.isFile(executable)) continue;
      return {
        node: path.join(resources, "runtime", "primary-runtime", "dependencies", "node", "bin", this.platform === "win32" ? "node.exe" : "node"),
        worker: path.join(resources, "runtime", "cli", "command-manager.js"),
        launcher: this.platform === "win32"
          ? path.join(resources, "runtime", "cli", "bin", "dsh.cmd")
          : "/usr/local/bin/dsh",
      };
    }
    return null;
  }

  private workerEnv(): NodeJS.ProcessEnv {
    return Object.fromEntries(Object.entries(this.env).filter(([key]) =>
      !/KEY|SECRET|TOKEN|PASSWORD|^NODE_OPTIONS$|^NODE_PATH$/iu.test(key),
    ));
  }

  private async worker(installation: Installation, operation: "inspect" | "install", fingerprint?: string, elevated = false): Promise<CommandState> {
    const args = [installation.worker, operation, ...fingerprint ? [fingerprint] : []];
    // 2026-10-08 coder(lq): Elevated paths are fixed by discovery, passed as argv, and confirmed with DSH's stale-state fingerprint.
    const output = elevated
      ? await this.run("/usr/bin/osascript", ["-e", 'on run argv\nset cmd to "/usr/bin/env -i " & quoted form of (item 1 of argv) & " " & quoted form of (item 2 of argv) & " " & quoted form of (item 3 of argv) & " " & quoted form of (item 4 of argv)\ndo shell script cmd with administrator privileges\nend run', installation.node, ...args], this.workerEnv())
      : await this.run(installation.node, args, this.workerEnv());
    const result: unknown = JSON.parse(output.trim());
    if (!result || typeof result !== "object" || !("ok" in result) || result.ok !== true || !("state" in result)) {
      const code = result && typeof result === "object" && "code" in result ? result.code : undefined;
      throw Object.assign(new Error("DSH command management failed"), { code });
    }
    const state = result.state;
    if (!state || typeof state !== "object" || !("fingerprint" in state) || typeof state.fingerprint !== "string"
      || !/^[a-f0-9]{64}$/u.test(state.fingerprint) || !("managed" in state) || typeof state.managed !== "boolean"
      || !("available" in state) || typeof state.available !== "boolean") throw new Error("Invalid DSH command state");
    return {
      fingerprint: state.fingerprint,
      managed: state.managed,
      available: state.available,
      shadowed: "activeCommand" in state && typeof state.activeCommand === "string"
        && (this.platform === "win32" ? win32.normalize(state.activeCommand).toLowerCase() !== win32.normalize(installation.launcher).toLowerCase() : state.activeCommand !== installation.launcher),
      occupied: ("kind" in state && state.kind !== "missing") || ("activeCommand" in state && typeof state.activeCommand === "string"),
    };
  }

  private async verify(candidate: string): Promise<boolean> {
    try {
      const args = ["--profile", "multica", "--probe"];
      const output = this.platform === "win32"
        ? await this.run(win32.join(this.env.SystemRoot ?? this.env.WINDIR ?? "C:\\Windows", "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
          ["-NoProfile", "-NonInteractive", "-Command", "& $env:MISSIONOS_DSH_LAUNCHER --profile multica --probe; exit $LASTEXITCODE"],
          { ...this.workerEnv(), MISSIONOS_DSH_LAUNCHER: candidate })
        : await this.run(candidate, args, this.workerEnv());
      const frame: unknown = JSON.parse(output.trim());
      return Boolean(frame && typeof frame === "object" && "v" in frame && frame.v === 1
        && "type" in frame && frame.type === "probe" && "runtime" in frame && frame.runtime === "dsh"
        && "protocol_version" in frame && frame.protocol_version === 1);
    } catch { return false; }
  }

  private async currentCommand(): Promise<string | null> {
    const path = this.platform === "win32" ? win32 : posix;
    const names = this.platform === "win32" ? ["dsh.exe", "dsh.cmd", "dsh.bat"] : ["dsh"];
    for (const directory of (this.env.PATH ?? "").split(this.platform === "win32" ? ";" : ":").filter(Boolean)) {
      for (const name of names) {
        const candidate = path.join(directory, name);
        if (!await this.isFile(candidate)) continue;
        return candidate;
      }
    }
    return null;
  }

  private async check(repair: boolean): Promise<DshCliStatus> {
    try {
      if (this.platform !== "darwin" && this.platform !== "win32") return { state: "unsupported" };
      const installation = await this.locate();
      if (!installation) return { state: "not_installed" };
      // 2026-10-08 coder(lq): A Homebrew/user wrapper may launch the same DSH app; validate its protocol, not its file identity.
      const currentCommand = await this.currentCommand();
      if (currentCommand) {
        if (await this.verify(currentCommand)) return { state: "ready" };
        const isRegisteredLauncher = this.platform === "win32"
          ? win32.normalize(currentCommand).toLowerCase() === win32.normalize(installation.launcher).toLowerCase()
          : currentCommand === installation.launcher;
        if (!isRegisteredLauncher) return { state: "error", reason: "probe_failed" };
      }
      if (!await this.isFile(installation.node) || !await this.isFile(installation.worker)) return { state: "unsupported" };
      let state = await this.worker(installation, "inspect");
      if (state.shadowed || (!state.managed && state.occupied)) return { state: "error", reason: "command_conflict" };
      if (!currentCommand && state.available && await this.verify(installation.launcher)) {
        this.refreshPath(installation);
        return { state: "ready" };
      }
      if (!repair) return { state: "needs_repair" };
      try { await this.worker(installation, "install", state.fingerprint); }
      catch (error) {
        const code = (error as NodeJS.ErrnoException).code;
        if (this.platform !== "darwin" || !["EACCES", "EPERM"].includes(code ?? "")) throw error;
        await this.worker(installation, "install", state.fingerprint, true);
      }
      state = await this.worker(installation, "inspect");
      if (state.shadowed) return { state: "error", reason: "command_conflict" };
      if (!state.managed || !state.available) return { state: "error", reason: "registration_failed" };
      if (!await this.verify(installation.launcher)) return { state: "error", reason: "probe_failed" };
      this.refreshPath(installation);
      return { state: "ready" };
    } catch (error) {
      if (["EACCES", "EPERM"].includes((error as NodeJS.ErrnoException).code ?? "")) {
        return { state: "error", reason: "permission_denied" };
      }
      return { state: "error" };
    }
  }

  private refreshPath(installation: Installation): void {
    // 2026-10-08 coder(lq): Windows registry PATH changes do not refresh this running GUI; append only, without shadowing other CLIs.
    const path = this.platform === "win32" ? win32 : { dirname };
    this.env.PATH = appendMissingPathDirs(this.env.PATH ?? "", [path.dirname(installation.launcher)], this.platform === "win32" ? ";" : ":");
  }
}
