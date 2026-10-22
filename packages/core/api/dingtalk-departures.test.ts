// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());
function respond(body: unknown) {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

describe("DingTalk departure sync boundary", () => {
  it("defaults old servers to not checked, never successful", async () => {
    respond({ organizations_created: 2 });
    expect(await new ApiClient("").syncProjectAuthorizationDingTalk("w1")).toMatchObject({ organizations_created: 2, departure_sync_status: "not_checked", accounts_disabled: 0 });
  });
  it("sends no personal identity or credentials and preserves application sync counts", async () => {
    const fetch = respond({ departure_sync_status: "complete", accounts_disabled: 2, accounts_already_disabled: 1, departures_skipped: 3 });
    const result = await new ApiClient("").syncProjectAuthorizationDingTalk("w1");
    expect(result).toMatchObject({ departure_sync_status: "complete", accounts_disabled: 2, accounts_already_disabled: 1, departures_skipped: 3 });
    expect(fetch).toHaveBeenCalledWith("/api/workspaces/w1/projectauth/organizations/sync", expect.objectContaining({ method: "POST", body: "{}" }));
  });
  it("does not accept unknown status as successful departure verification", async () => {
    respond({ departure_sync_status: "unexpected" });
    expect((await new ApiClient("").syncProjectAuthorizationDingTalk("w1")).departure_sync_status).toBe("unavailable");
  });
  it.each([null, { accounts_disabled: "5" }, { departures_skipped: -1 }])("does not trust malformed counts %j", async (body) => {
    respond(body);
    expect(await new ApiClient("").syncProjectAuthorizationDingTalk("w1")).toMatchObject({ departure_sync_status: "not_checked", accounts_disabled: 0 });
  });
});
