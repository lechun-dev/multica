// @vitest-environment node

import { describe, expect, it } from "vitest";
import { parseTrailingDwsJSON } from "./dws-auth-manager";

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
