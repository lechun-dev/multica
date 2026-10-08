// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { chmod, mkdtemp, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DshCliManager, dshDesktopCandidates, probeDshACP } from "./dsh-cli";

const bundled = "/Applications/DeepSeek Harness.app/Contents/Resources/runtime/cli/bin/dsh";
const response = '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}';
function fixture(files = [bundled], env: NodeJS.ProcessEnv = { PATH: "/test-bin" }) {
  const probe = vi.fn(async () => {});
  const manager = new DshCliManager({ platform: "darwin", home: "/test-home", env, probe, isFile: async (file) => files.includes(file) });
  return { manager, probe, env };
}

describe("official DSH launcher selection", () => {
  it("automatically prepares the bundled launcher without modifying PATH", async () => {
    const { manager, probe, env } = fixture();
    expect(await manager.inspect()).toEqual({ state: "ready" });
    expect(probe).toHaveBeenCalledWith(bundled, env, 4000);
    expect(env.MULTICA_DSH_PATH).toBe(bundled);
    expect(env.PATH).toBe("/test-bin");
  });
  it("uses a compatible PATH wrapper before the App launcher", async () => {
    const { manager, probe, env } = fixture(["/test-bin/dsh", bundled]);
    expect(await manager.repair()).toEqual({ state: "ready" });
    expect(probe).toHaveBeenCalledTimes(1);
    expect(env.MULTICA_DSH_PATH).toBe("/test-bin/dsh");
  });
  it("falls back to the official launcher without overwriting a broken PATH command", async () => {
    const { manager, probe, env } = fixture(["/test-bin/dsh", bundled]);
    probe.mockRejectedValueOnce(new Error("incompatible"));
    expect(await manager.inspect()).toEqual({ state: "ready" });
    expect(env.MULTICA_DSH_PATH).toBe(bundled);
    expect(env.PATH).toBe("/test-bin");
  });
  it("never replaces an explicit operator override", async () => {
    const { manager, probe, env } = fixture([bundled], { PATH: "", MULTICA_DSH_PATH: "/custom/dsh" });
    probe.mockRejectedValue(new Error("missing"));
    expect(await manager.inspect()).toEqual({ state: "error", reason: "probe_failed" });
    expect(probe).toHaveBeenCalledTimes(1);
    expect(env.MULTICA_DSH_PATH).toBe("/custom/dsh");
  });
  it("preserves an operator override when a later probe fails", async () => {
    const { manager, probe, env } = fixture([], { MULTICA_DSH_PATH: "/explicit/dsh" });
    expect(await manager.inspect()).toEqual({ state: "ready" });
    probe.mockRejectedValue(new Error("upgrade window"));
    expect(await manager.inspect()).toEqual({ state: "error", reason: "probe_failed" });
    expect(env.MULTICA_DSH_PATH).toBe("/explicit/dsh");
  });
  it("does not keep a stale selection when the App is removed", async () => {
    const files = [bundled];
    const { manager, env } = fixture(files);
    await manager.inspect(); files.length = 0;
    expect(await manager.inspect()).toEqual({ state: "not_installed" });
    expect(env.MULTICA_DSH_PATH).toBeUndefined();
  });
  it("serializes automatic preparation and user rechecks", async () => {
    const { manager, probe } = fixture();
    await Promise.all([manager.inspect(), manager.inspect(), manager.repair()]);
    expect(probe).toHaveBeenCalledTimes(1);
  });
  it("only prompts installation when neither App nor CLI exists", async () => {
    const { manager, probe } = fixture([]);
    expect(await manager.inspect()).toEqual({ state: "not_installed" });
    expect(probe).not.toHaveBeenCalled();
  });
  it("works with an existing Linux CLI", async () => {
    const env = { PATH: "/fake" };
    const manager = new DshCliManager({ platform: "linux", env, probe: async () => {}, isFile: async (p) => p === "/fake/dsh" });
    expect(await manager.inspect()).toEqual({ state: "ready" });
  });
  it("does not misclassify ProgramFiles when LOCALAPPDATA is missing", () => {
    expect(dshDesktopCandidates("win32", "C:\\Users\\test", { ProgramFiles: "C:\\Programs" })).toEqual([
      "C:\\Users\\test\\AppData\\Local\\Programs\\DeepSeek Harness\\resources",
      "C:\\Programs\\DeepSeek Harness\\resources",
    ]);
  });
});

describe.runIf(process.platform !== "win32")("bounded ACP process checks (fake CLIs only)", () => {
  async function script(body: string, run: (file: string) => Promise<void>) {
    const home = await mkdtemp(join(tmpdir(), "missionos-dsh-acp-test-"));
    try {
      const file = join(home, "dsh");
      await writeFile(file, `#!/bin/sh\n[ "$*" = "--profile acp" ] || exit 2\nIFS= read -r line\n${body}\n`);
      await chmod(file, 0o700); await run(file);
    } finally { await rm(home, { recursive: true, force: true }); }
  }
  it("accepts initialize without waiting for CLI exit or creating a session", async () => {
    await script(`printf '%s\\n' '${response}'\n/bin/sleep 30`, async (file) => {
      await expect(probeDshACP(file, { PATH: "" }, 1500)).resolves.toBeUndefined();
    });
  });
  it("reports incompatible ACP separately from a timeout", async () => {
    await script(`printf '%s\\n' '${response.replace('Version":1', 'Version":9')}'`, async (file) => {
      await expect(probeDshACP(file, {}, 1500)).rejects.toMatchObject({ reason: "protocol_incompatible" });
    });
  });
  it("reports an initialize RPC error as a failed handshake, not an incompatible version", async () => {
    await script(`printf '%s\\n' '{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"SECRET_TEST_VALUE"}}'`, async (file) => {
      await expect(probeDshACP(file, {}, 1500)).rejects.toMatchObject({ reason: "probe_failed", message: "probe_failed" });
    });
  });
  it("bounds hanging children and returns a timeout", async () => {
    await script("/bin/sleep 30 & wait", async (file) => {
      const start = Date.now();
      await expect(probeDshACP(file, {}, 100)).rejects.toMatchObject({ reason: "probe_timeout" });
      expect(Date.now() - start).toBeLessThan(1500);
    });
  });
  it("does not forward subprocess stderr", async () => {
    await script("printf 'SECRET_TEST_VALUE' >&2; exit 1", async (file) => {
      await expect(probeDshACP(file, {}, 1500)).rejects.toMatchObject({ message: "probe_failed" });
    });
  });
  it("classifies a missing executable as startup failure", async () => {
    await expect(probeDshACP("/nonexistent/test-dsh", {}, 1500)).rejects.toMatchObject({ reason: "launch_failed" });
  });
});
