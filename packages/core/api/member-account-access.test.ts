import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
const member = { id: "m1", user_id: "u1", workspace_id: "w1", role: "member" };
afterEach(() => vi.unstubAllGlobals());
function respond(body: unknown) {
 const mock = vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
 vi.stubGlobal("fetch", mock); return mock;
}
describe("account access API boundary", () => {
 it("defaults old-server flags closed", async () => {
  respond([member]);
  const [item] = await new ApiClient("").listMembers("w1");
  expect(item?.account_disabled).toBe(false); expect(item?.can_manage_account).toBe(false);
 });
 it("does not accept malformed member data as management authority", async () => {
  respond([{ ...member, can_manage_account: "yes" }]);
  expect(await new ApiClient("").listMembers("w1")).toEqual([]);
 });
 it("sends only the requested account state and verifies the response", async () => {
  const mock = respond({ ...member, account_disabled: true });
  await new ApiClient("").setMemberAccountAccess("w1", "m1", true);
  expect(mock).toHaveBeenCalledWith("/api/workspaces/w1/members/m1/account-access", expect.objectContaining({ method: "PATCH", body: '{"disabled":true}' }));
 });
 it.each([null, {}, { ...member, account_disabled: false }, { ...member, id: "wrong", account_disabled: true }])("rejects an unverifiable mutation response %j", async (body) => {
  respond(body);
  await expect(new ApiClient("").setMemberAccountAccess("w1", "m1", true)).rejects.toThrow("could not be verified");
 });
});
