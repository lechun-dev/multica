// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { chmod, mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
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
  path?: string;
} = {}) {
  let installed = false;
  const env = { PATH: options.path ?? "/existing", LOCALAPPDATA: "C:\\Users\\tester\\AppData\\Local" };
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
  it("probes a test-created executable wrapper through the real process runner", async () => {
    const home = await mkdtemp(join(tmpdir(), "missionos-dsh-process-test-"));
    try {
      const candidate = join(home, "dsh");
      await writeFile(candidate, `#!/bin/sh
[ "$1" = "--profile" ] && [ "$2" = "multica" ] && [ "$3" = "--probe" ] || exit 2
printf '%s\\n' '${probe}'
`);
      await chmod(candidate, 0o700);
      const env = { PATH: home };
      const manager = new DshCliManager({
        platform: "darwin", home, env,
        // 2026-10-08 coder(lq): The process runner executes only this temporary wrapper, not any installed agent CLI.
        isFile: async (path) => path.startsWith(home),
      });
      expect(await manager.inspect()).toEqual({ state: "ready" });
      expect(await manager.repair()).toEqual({ state: "ready" });
      expect(env.PATH).toBe(home);
    } finally { await rm(home, { recursive: true, force: true }); }
  });

  it("accepts a usable wrapper at a different path without installing or replacing it", async () => {
    const home = await mkdtemp(join(tmpdir(), "missionos-dsh-wrapper-test-"));
    try {
      const resources = join(home, "Applications", "DeepSeek Harness.app", "Contents", "Resources");
      const commandDir = join(home, "bin");
      const candidate = join(commandDir, "dsh");
      const launcher = join(resources, "runtime", "cli", "bin", "dsh");
      await mkdir(commandDir, { recursive: true });
      await mkdir(join(resources, "runtime", "cli", "bin"), { recursive: true });
      await writeFile(launcher, "fake bundled CLI");
      await writeFile(candidate, "#!/bin/sh\n# fake wrapper, execution is injected\n");
      const env = { PATH: commandDir };
      const run = vi.fn(async () => probe);
      const manager = new DshCliManager({
        platform: "darwin", home, env, run,
        isFile: async (path) => path.startsWith(home),
      });
      expect(await manager.inspect()).toEqual({ state: "ready" });
      expect(await manager.repair()).toEqual({ state: "ready" });
      expect(run).toHaveBeenCalledTimes(2);
      expect(run).toHaveBeenCalledWith(candidate, ["--profile", "multica", "--probe"], expect.any(Object));
      expect(env.PATH).toBe(commandDir);
    } finally { await rm(home, { recursive: true, force: true }); }
  });

  it("reports failed verification without replacing an existing PATH command", async () => {
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
        // 2026-10-08 coder(lq): Discover only test-created paths, never the user's installed agent app.
        isFile: async (path) => path.startsWith(home),
      });
      expect(await manager.repair()).toEqual({ state: "error", reason: "probe_failed" });
      expect(run).toHaveBeenCalledTimes(1);
      expect(run).toHaveBeenCalledWith(join(commandDir, "dsh"), ["--profile", "multica", "--probe"], expect.any(Object));
    } finally { await rm(home, { recursive: true, force: true }); }
  });

  it("preserves a different Windows command even if DSH owns its directory", async () => {
    const state = JSON.stringify({ ok: true, state: {
      fingerprint, managed: true, available: true, activeCommand: "C:\\Other\\dsh.cmd",
    } });
    const { manager, run } = fixture({ platform: "win32", initial: state });
    expect(await manager.repair()).toEqual({ state: "error", reason: "command_conflict" });
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("keeps repair available for a broken DSH-owned registered launcher", async () => {
    const { manager, run } = fixture({ path: "/usr/local/bin" });
    let repaired = false;
    run.mockImplementation(async (_file, args) => {
      if (args.includes("install")) { repaired = true; return worker(); }
      if (args.includes("inspect")) return worker();
      return repaired ? probe : "broken launcher";
    });
    expect(await manager.inspect()).toEqual({ state: "needs_repair" });
    expect(run.mock.calls.some(([, args]) => args.includes("install"))).toBe(false);
    expect(await manager.repair()).toEqual({ state: "ready" });
    expect(run.mock.calls.filter(([, args]) => args.includes("install"))).toHaveLength(1);
  });

  it("does not replace an unowned command at the registered launcher path", async () => {
    const { manager, run } = fixture({ path: "/usr/local/bin" });
    run.mockImplementation(async (_file, args) => args.includes("inspect")
      ? worker(false, true, "file") : "unrelated CLI");
    expect(await manager.repair()).toEqual({ state: "error", reason: "command_conflict" });
    expect(run.mock.calls.some(([, args]) => args.includes("install"))).toBe(false);
  });

  it("accepts an existing Windows wrapper via a fixed PowerShell invocation without registration", async () => {
    const { manager, run, env } = fixture({ platform: "win32", path: "C:\\Custom tools" });
    expect(await manager.inspect()).toEqual({ state: "ready" });
    expect(await manager.repair()).toEqual({ state: "ready" });
    expect(run).toHaveBeenCalledTimes(2);
    expect(run).toHaveBeenCalledWith(expect.stringContaining("powershell.exe"),
      expect.arrayContaining(["& $env:MISSIONOS_DSH_LAUNCHER --profile multica --probe; exit $LASTEXITCODE"]),
      expect.objectContaining({ MISSIONOS_DSH_LAUNCHER: "C:\\Custom tools\\dsh.exe" }));
    expect(env.PATH).toBe("C:\\Custom tools");
  });

  it("reports failed startup without leaking the error or registering over a custom command", async () => {
    const { manager, run } = fixture({ path: "/custom" });
    run.mockRejectedValue(new Error("sensitive diagnostic"));
    expect(await manager.repair()).toEqual({ state: "error", reason: "probe_failed" });
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("distinguishes denied registration permissions from an invalid probe", async () => {
    const { manager, run } = fixture({ platform: "win32", initial: worker(false, false, "missing") });
    run.mockImplementation(async (_file, args) => {
      if (args.includes("install")) throw Object.assign(new Error("denied"), { code: "EACCES" });
      return worker(false, false, "missing");
    });
    expect(await manager.repair()).toEqual({ state: "error", reason: "permission_denied" });
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
    expect(await manager.repair()).toEqual({ state: "error", reason: "command_conflict" });
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("rejects invalid worker output without a mutation", async () => {
    const { manager, run } = fixture({ initial: JSON.stringify({ ok: true, state: { fingerprint: "bad" } }) });
    expect(await manager.repair()).toEqual({ state: "error" });
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("does not report success if registration or protocol verification fails", async () => {
    expect(await fixture({ initial: worker(false, false, "missing"), after: worker(true, false) }).manager.repair()).toEqual({ state: "error", reason: "registration_failed" });
    expect(await fixture({ probe: '{"v":1,"type":"probe","runtime":"other","protocol_version":1}' }).manager.repair()).toEqual({ state: "error", reason: "probe_failed" });
    expect(await fixture({ probe: "not JSON" }).manager.repair()).toEqual({ state: "error", reason: "probe_failed" });
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
