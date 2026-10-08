// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DshCliManager, dshDesktopCandidates } from "./dsh-cli";

const fingerprint = "a".repeat(64);
const probe = JSON.stringify({ v: 1, type: "probe", runtime: "dsh", protocol_version: 1 });
function worker(managed = true, available = true, kind = "symlink") {
  return JSON.stringify({ ok: true, state: { fingerprint, managed, available, kind } });
}
function fixture(options: {
  installed?: boolean;
  workerPresent?: boolean;
  initial?: string;
  after?: string;
  probe?: string;
  platform?: NodeJS.Platform;
} = {}) {
  let installed = false;
  const env = { PATH: "/existing", LOCALAPPDATA: "C:\\Users\\tester\\AppData\\Local" };
  const run = vi.fn(async (_file: string, args: string[]) => {
    if (args.includes("install")) { installed = true; return worker(); }
    if (args.includes("inspect")) return installed ? options.after ?? worker() : options.initial ?? worker();
    return options.probe ?? probe;
  });
  const manager = new DshCliManager({
    platform: options.platform ?? "darwin", home: "/Users/tester", env, run,
    isFile: vi.fn(async (path) => !path.startsWith("/existing/") && !path.startsWith("\\existing\\") && options.installed !== false && (options.workerPresent !== false || !path.endsWith("command-manager.js"))),
  });
  return { manager, run, env };
}

describe("DSH desktop CLI setup", () => {
  it("does not mutate when a PATH command shadows the installed launcher", async () => {
    const home = await mkdtemp(join(tmpdir(), "missionos-dsh-test-"));
    try {
      const resources = join(home, "Applications", "DeepSeek Harness.app", "Contents", "Resources");
      const commandDir = join(home, "bin");
      const launcher = join(resources, "runtime", "cli", "bin", "dsh");
      await mkdir(commandDir, { recursive: true });
      await mkdir(join(resources, "runtime", "cli", "bin"), { recursive: true });
      await writeFile(launcher, "fake bundled CLI");
      await writeFile(join(commandDir, "dsh"), "unrelated CLI");
      const run = vi.fn(async () => worker());
      const manager = new DshCliManager({
        platform: "darwin", home, env: { PATH: commandDir }, run,
        // Only test-created paths are discovered; no real /Applications app is inspected.
        isFile: async (path) => path.startsWith(home),
      });
      expect(await manager.repair()).toEqual({ state: "error" });
      expect(run).not.toHaveBeenCalled();
    } finally { await rm(home, { recursive: true, force: true }); }
  });

  it("preserves a different Windows command even if DSH owns its directory", async () => {
    const state = JSON.stringify({ ok: true, state: {
      fingerprint, managed: true, available: true, activeCommand: "C:\\Other\\dsh.cmd",
    } });
    const { manager, run } = fixture({ platform: "win32", initial: state });
    expect(await manager.repair()).toEqual({ state: "error" });
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("does not treat stale confirmation or failed writes as success", async () => {
    const { manager, run } = fixture({ initial: worker(false, false, "missing") });
    run.mockImplementation(async (_file, args) => args.includes("install")
      ? JSON.stringify({ ok: false, code: "ESTALE" }) : worker(false, false, "missing"));
    expect(await manager.repair()).toEqual({ state: "error" });
    expect(run.mock.calls.filter(([, args]) => args.includes("install"))).toHaveLength(1);
  });

  it("uses native macOS authorization only after a permission failure", async () => {
    const { manager, run } = fixture({ initial: worker(false, false, "missing") });
    let registered = false;
    run.mockImplementation(async (file, args) => {
      if (file === "/usr/bin/osascript") { registered = true; return worker(); }
      if (args.includes("install")) return JSON.stringify({ ok: false, code: "EACCES" });
      if (args.includes("inspect")) return registered ? worker() : worker(false, false, "missing");
      return probe;
    });
    expect(await manager.repair()).toEqual({ state: "ready" });
    expect(run).toHaveBeenCalledWith("/usr/bin/osascript", expect.arrayContaining([fingerprint]), expect.any(Object));
  });

  it("returns an error when native authorization is cancelled", async () => {
    const { manager, run } = fixture({ initial: worker(false, false, "missing") });
    run.mockImplementation(async (file, args) => {
      if (file === "/usr/bin/osascript") throw new Error("cancelled");
      return args.includes("install") ? JSON.stringify({ ok: false, code: "EACCES" }) : worker(false, false, "missing");
    });
    expect(await manager.repair()).toEqual({ state: "error" });
  });

  it("discovers current macOS and Windows default installations", () => {
    expect(dshDesktopCandidates("darwin", "/Users/a", {})).toContain("/Users/a/Applications/DeepSeek Harness.app/Contents/Resources");
    expect(dshDesktopCandidates("win32", "C:\\Users\\a", { ProgramFiles: "D:\\Apps" })).toEqual([
      "C:\\Users\\a\\AppData\\Local\\Programs\\DeepSeek Harness\\resources",
      "D:\\Apps\\DeepSeek Harness\\resources",
    ]);
    expect(dshDesktopCandidates("linux", "/home/a", {})).toEqual([]);
  });

  it("does not execute anything when DSH is absent, including repair requests", async () => {
    const { manager, run } = fixture({ installed: false });
    expect(await manager.inspect()).toEqual({ state: "not_installed" });
    expect(await manager.repair()).toEqual({ state: "not_installed" });
    expect(run).not.toHaveBeenCalled();
  });

  it("distinguishes an older installed app from an absent app", async () => {
    const { manager, run } = fixture({ workerPresent: false });
    expect(await manager.repair()).toEqual({ state: "unsupported" });
    expect(run).not.toHaveBeenCalled();
  });

  it("does not install on unsupported platforms", async () => {
    const { manager, run } = fixture({ platform: "linux" });
    expect(await manager.repair()).toEqual({ state: "unsupported" });
    expect(run).not.toHaveBeenCalled();
  });

  it("checks protocol discovery, not just the existence of the launcher", async () => {
    const { manager, run, env } = fixture();
    expect(await manager.inspect()).toEqual({ state: "ready" });
    expect(run).toHaveBeenCalledWith("/usr/local/bin/dsh", ["--profile", "multica", "--probe"], expect.any(Object));
    expect(env.PATH).toBe("/existing:/usr/local/bin");
    expect(run.mock.calls.some(([, args]) => args.includes("install"))).toBe(false);
  });

  it("offers repair for a missing command without automatically writing", async () => {
    const { manager, run } = fixture({ initial: worker(false, false, "missing") });
    expect(await manager.inspect()).toEqual({ state: "needs_repair" });
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("uses the installed worker and its fingerprint, then verifies again", async () => {
    const { manager, run } = fixture({ initial: worker(false, false, "missing") });
    expect(await manager.repair()).toEqual({ state: "ready" });
    expect(run.mock.calls.filter(([, args]) => args.includes("inspect"))).toHaveLength(2);
    expect(run).toHaveBeenCalledWith(expect.stringContaining("node/bin/node"), [expect.stringContaining("runtime/cli/command-manager.js"), "install", fingerprint], expect.any(Object));
  });

  it("never overwrites an unowned command", async () => {
    const { manager, run } = fixture({ initial: worker(false, true, "file") });
    expect(await manager.repair()).toEqual({ state: "error" });
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("rejects invalid worker output without a mutation", async () => {
    const { manager, run } = fixture({ initial: JSON.stringify({ ok: true, state: { fingerprint: "bad" } }) });
    expect(await manager.repair()).toEqual({ state: "error" });
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("does not report success if registration or protocol verification fails", async () => {
    expect(await fixture({ initial: worker(false, false, "missing"), after: worker(true, false) }).manager.repair()).toEqual({ state: "error" });
    expect(await fixture({ probe: '{"v":1,"type":"probe","runtime":"other","protocol_version":1}' }).manager.repair()).toEqual({ state: "error" });
    expect(await fixture({ probe: "not JSON" }).manager.repair()).toEqual({ state: "error" });
  });

  it("deduplicates concurrent repair requests", async () => {
    const { manager, run } = fixture({ initial: worker(false, false, "missing") });
    const first = manager.repair();
    expect(manager.repair()).toBe(first);
    await first;
    expect(run.mock.calls.filter(([, args]) => args.includes("install"))).toHaveLength(1);
  });

  it("uses PowerShell with a fixed command, never interpolating Windows paths", async () => {
    const { manager, run, env } = fixture({ platform: "win32" });
    expect(await manager.inspect()).toEqual({ state: "ready" });
    expect(run).toHaveBeenCalledWith(expect.stringContaining("powershell.exe"), expect.arrayContaining(["& $env:MISSIONOS_DSH_LAUNCHER --profile multica --probe; exit $LASTEXITCODE"]), expect.objectContaining({ MISSIONOS_DSH_LAUNCHER: expect.stringContaining("dsh.cmd") }));
    expect(env.PATH).toContain(";C:\\Users\\tester\\AppData\\Local\\Programs\\DeepSeek Harness\\resources\\runtime\\cli\\bin");
  });
});
