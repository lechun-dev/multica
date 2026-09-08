import { describe, expect, it } from "vitest";
import { DEV_PROTOCOL, ensureDevProtocol } from "./brand-dev-protocol.mjs";

describe("ensureDevProtocol", () => {
  it("registers the isolated local-development callback protocol", () => {
    expect(ensureDevProtocol(undefined, "ai.multica.desktop.dev")).toEqual([
      {
        CFBundleTypeRole: "Editor",
        CFBundleURLName: "ai.multica.desktop.dev",
        CFBundleURLSchemes: [DEV_PROTOCOL],
      },
    ]);
  });

  it("preserves existing handlers and remains idempotent", () => {
    const existing = [
      {
        CFBundleTypeRole: "Viewer",
        CFBundleURLName: "example",
        CFBundleURLSchemes: ["example"],
      },
      {
        CFBundleTypeRole: "Editor",
        CFBundleURLName: "ai.multica.desktop.dev",
        CFBundleURLSchemes: [DEV_PROTOCOL],
      },
    ];

    expect(ensureDevProtocol(existing, "ai.multica.desktop.dev")).toBe(existing);
  });
});
