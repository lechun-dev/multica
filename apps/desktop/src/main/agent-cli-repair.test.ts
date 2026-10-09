// @vitest-environment node
import { describe, expect, it, vi } from "vitest";

import {
  codexCandidatesForSelection,
  codexDiscoveryCandidates,
  findUsableCodexCli,
  isAgentCliMissingError,
  verifyCodexCli,
} from "./agent-cli-repair";

const { execFileMock } = vi.hoisted(() => ({
  execFileMock: vi.fn<
    (
      file: string,
      args: string[],
      options: unknown,
      callback: (error: Error | null, stdout: string, stderr: string) => void,
    ) => void
  >(),
}));
vi.mock("child_process", () => ({ execFile: execFileMock }));

describe("agent CLI startup error detection", () => {
  it("matches only the daemon's no-agent startup failure", () => {
    expect(
      isAgentCliMissingError(
        "daemon exited during startup: no agent CLI found: install codex",
      ),
    ).toBe(true);
    expect(isAgentCliMissingError("connection refused")).toBe(false);
  });
});

describe("Codex CLI discovery", () => {
  it("expands a selected macOS app bundle to its bundled CLI", () => {
    expect(codexCandidatesForSelection("/Custom/ChatGPT.app")).toEqual([
      "/Custom/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex",
      "/Custom/ChatGPT.app/Contents/Resources/codex-cli/bin/codex",
      "/Custom/ChatGPT.app/Contents/Resources/codex",
    ]);
  });

  it("keeps a directly selected executable unchanged", () => {
    expect(codexCandidatesForSelection("/opt/homebrew/bin/codex")).toEqual([
      "/opt/homebrew/bin/codex",
    ]);
  });

  it("includes PATH and standard app bundles without duplicates", async () => {
    const candidates = await codexDiscoveryCandidates({
      home: "/Users/tester",
      env: { PATH: "/custom/bin:/opt/homebrew/bin" },
      platform: "darwin",
    });

    expect(candidates).toContain("/custom/bin/codex");
    const nestedChatGPT =
      "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex";
    const flatChatGPT = "/Applications/ChatGPT.app/Contents/Resources/codex";
    expect(candidates).toContain(nestedChatGPT);
    const binChatGPT = "/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex";
    expect(candidates).toContain(binChatGPT);
    expect(candidates.indexOf(binChatGPT)).toBeLessThan(candidates.indexOf(flatChatGPT));
    expect(candidates).toContain(flatChatGPT);
    expect(candidates.indexOf(nestedChatGPT)).toBeLessThan(
      candidates.indexOf(flatChatGPT),
    );
    expect(candidates.indexOf(flatChatGPT)).toBeLessThan(
      candidates.indexOf("/custom/bin/codex"),
    );
    expect(
      candidates.indexOf(
        "/Users/tester/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex",
      ),
    ).toBeLessThan(candidates.indexOf(flatChatGPT));
    expect(new Set(candidates).size).toBe(candidates.length);
  });

  it.each(["/custom/codex", "codex"])(
    "keeps explicit override %s authoritative",
    async (explicit) => {
      expect(
        await codexDiscoveryCandidates({
          home: "/Users/tester",
          env: { MULTICA_CODEX_PATH: explicit, PATH: "/npm/bin" },
          platform: "darwin",
        }),
      ).toEqual([explicit]);
    },
  );

  it("preserves PATH priority outside macOS", async () => {
    const candidates = await codexDiscoveryCandidates({
      home: "/home/tester",
      env: { PATH: "/npm/bin" },
      platform: "linux",
    });
    expect(candidates[0]).toBe("/npm/bin/codex");
    expect(candidates.some((path) => path.includes("ChatGPT.app"))).toBe(false);
  });

  it.each(["flat", "PATH"])(
    "falls back from broken nested bundles to %s",
    async (fallback) => {
      const candidates = await codexDiscoveryCandidates({
        home: "/Users/tester",
        env: { PATH: "/npm/bin" },
        platform: "darwin",
      });
      const expected =
        fallback === "flat"
          ? "/Applications/ChatGPT.app/Contents/Resources/codex"
          : "/npm/bin/codex";
      const verify = vi.fn(async (path: string) => path === expected);
      expect(await findUsableCodexCli(candidates, verify)).toBe(expected);
      expect(verify.mock.calls[0]?.[0]).toContain("codex-cli/CodexCLI.app");
    },
  );

  it.each([
    ["codex-cli 0.162.0-alpha.17.2", "", true],
    ["", "codex-cli 0.150.1", true],
    ["codex-cli v0.150.1", "", true],
    ["", "", false],
    ["not a version", "", false],
  ])("checks readable version output %s", async (stdout, stderr, usable) => {
    execFileMock.mockImplementation((_file, _args, _options, callback) => {
      callback(null, String(stdout), String(stderr));
    });
    expect(await verifyCodexCli("/isolated/codex")).toBe(usable);
  });

  it("rejects a failing version command", async () => {
    execFileMock.mockImplementation((_file, _args, _options, callback) => {
      callback(new Error("broken bundle"), "codex-cli 0.162.0", "");
    });
    expect(await verifyCodexCli("/isolated/codex")).toBe(false);
  });

  it("returns the first candidate that can actually execute", async () => {
    const verify = vi.fn(async (candidate: string) => candidate === "/b/codex");

    await expect(
      findUsableCodexCli(["/a/codex", "/b/codex", "/c/codex"], verify),
    ).resolves.toBe("/b/codex");
    expect(verify).toHaveBeenCalledTimes(2);
  });

  it("returns null when none of the candidates can execute", async () => {
    await expect(
      findUsableCodexCli(["/a/codex"], async () => false),
    ).resolves.toBeNull();
  });
});
