import { describe, expect, it } from "vitest";
import { buildSteps, groupSteps } from "./build-steps";
import { humanizeTraceRow, humanizeTraceRows } from "./human-readable";

describe("humanizeTraceRow", () => {
  it("turns common tool calls into plain-language actions", () => {
    const rows = groupSteps(
      buildSteps([
        { seq: 1, type: "tool_use", tool: "Bash", input: { command: "pnpm test" } },
        { seq: 2, type: "tool_result", tool: "Bash", output: "ok" },
        { seq: 3, type: "tool_use", tool: "Read", input: { file_path: "src/app.ts" } },
        { seq: 4, type: "tool_use", tool: "Grep", input: { query: "permission" } },
        { seq: 5, type: "tool_use", tool: "Edit", input: { path: "src/app.ts", content: "next" } },
      ]),
    );

    expect(humanizeTraceRows(rows)).toMatchObject([
      { kind: "action", action: "command", subject: "pnpm test", completed: true },
      { kind: "action", action: "read", subject: "src/app.ts", completed: false },
      { kind: "action", action: "search", subject: "permission", completed: false },
      { kind: "action", action: "edit", subject: "src/app.ts", completed: false },
    ]);
  });

  it("retains message and error bodies", () => {
    const [message, thinking, error] = buildSteps([
      { seq: 1, type: "text", content: "I found the issue." },
      { seq: 2, type: "thinking", content: "Checking the config." },
      { seq: 3, type: "error", content: "The command failed." },
    ]);

    expect(humanizeTraceRow(message!)).toEqual({
      seq: 1,
      kind: "text",
      text: "I found the issue.",
    });
    expect(humanizeTraceRow(thinking!)).toEqual({
      seq: 2,
      kind: "thinking",
      text: "Checking the config.",
    });
    expect(humanizeTraceRow(error!)).toEqual({
      seq: 3,
      kind: "error",
      text: "The command failed.",
    });
  });

  it("summarizes grouped calls and reports whether all calls completed", () => {
    const rows = groupSteps(
      buildSteps([
        { seq: 1, type: "tool_use", tool: "Read", input: { path: "a.ts" } },
        { seq: 2, type: "tool_result", tool: "Read", output: "a" },
        { seq: 3, type: "tool_use", tool: "Read", input: { path: "b.ts" } },
        { seq: 4, type: "tool_result", tool: "Read", output: "b" },
        { seq: 5, type: "tool_use", tool: "Read", input: { path: "c.ts" } },
      ]),
    );

    expect(humanizeTraceRow(rows[0]!)).toMatchObject({
      kind: "group",
      action: "read",
      subject: "a.ts",
      tool: "Read",
      count: 3,
      completed: false,
    });
  });

  it("uses a generic action for unknown tools instead of dropping them", () => {
    const [step] = buildSteps([
      { seq: 9, type: "tool_use", tool: "browser.open", input: { url: "https://example.com" } },
    ]);

    expect(humanizeTraceRow(step!)).toMatchObject({
      kind: "action",
      action: "tool",
      tool: "browser.open",
      subject: "https://example.com",
      completed: false,
    });
  });
});
