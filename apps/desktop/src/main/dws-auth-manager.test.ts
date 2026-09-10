// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  dwsErrorIsUnauthenticated,
  parseTrailingDwsJSON,
} from "./dws-auth-manager";

describe("parseTrailingDwsJSON", () => {
  it("parses a plain JSON response", () => {
    expect(
      parseTrailingDwsJSON('{"success":true,"authenticated":true}'),
    ).toEqual({ success: true, authenticated: true });
  });

  it("parses the final JSON object after OAuth progress output", () => {
    expect(
      parseTrailingDwsJSON(
        'Waiting for authorization...\n{\n  "success": true,\n  "user_name": "Li Si"\n}',
      ),
    ).toEqual({ success: true, user_name: "Li Si" });
  });

  it("rejects output without a complete object", () => {
    expect(parseTrailingDwsJSON("Waiting for authorization...")).toBeNull();
  });
});

describe("dwsErrorIsUnauthenticated", () => {
  it.each([
    "DWS is not logged in",
    "当前未登录",
    "AUTH_TOKEN_EXPIRED",
    "USER_TOKEN_ILLEGAL",
    "token验证失败",
  ])("recognizes a definitive authentication error: %s", (message) => {
    expect(dwsErrorIsUnauthenticated(message)).toBe(true);
  });

  it.each([
    "acquiring file lock: timeout",
    "network request failed",
    "DWS returned an invalid response",
  ])("keeps a transient failure separate: %s", (message) => {
    expect(dwsErrorIsUnauthenticated(message)).toBe(false);
  });
});
