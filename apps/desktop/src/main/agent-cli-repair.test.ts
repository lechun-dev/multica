// @vitest-environment node
import { describe, expect, it, vi } from "vitest";

import {
  codexCandidatesForSelection,
  codexDiscoveryCandidates,
  findUsableCodexCli,
  isAgentCliMissingError,
} from "./agent-cli-repair";

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
    expect(candidates).toContain(
      "/Applications/ChatGPT.app/Contents/Resources/codex",
    );
    expect(new Set(candidates).size).toBe(candidates.length);
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
