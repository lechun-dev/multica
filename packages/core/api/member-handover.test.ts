// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { ProjectAuthorizationOrganizationMemberSchema } from "./schemas";

const source = "10000000-0000-4000-8000-000000000001";
const recipient = "10000000-0000-4000-8000-000000000002";
const previewId = "10000000-0000-4000-8000-000000000003";
const snapshot = {
  preview_id: previewId, source_user_id: source, recipient_user_id: recipient,
  manager_user_id: recipient, manager_name: "Leader", manager_status: "ready", manager_sync_status: "complete",
  projects: [{ id: "10000000-0000-4000-8000-000000000004", title: "Project", project_id: "", role: "owner", reassign: true, version: "2026-11-03 01:00:00+00" }],
  issues: [], expires_at: "2026-11-03T01:15:00Z", completed: false,
};
function respond(body: unknown) {
  const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
afterEach(() => vi.unstubAllGlobals());

describe("member handover API contract", () => {
  it("previews the default manager or an explicit receiver without executing a handover", async () => {
    const fetch = respond(snapshot);
    const client = new ApiClient("");
    expect(await client.previewMemberHandover("ws", "member")).toEqual(snapshot);
    expect(fetch).toHaveBeenLastCalledWith("/api/workspaces/ws/members/member/handover/preview", expect.objectContaining({ method: "POST", body: "{}" }));
    await client.previewMemberHandover("ws", "member", recipient);
    expect(fetch).toHaveBeenLastCalledWith(expect.any(String), expect.objectContaining({ body: JSON.stringify({ recipient_user_id: recipient }) }));
  });

  it.each([
    null, {}, { ...snapshot, projects: undefined }, { ...snapshot, completed: true },
    { ...snapshot, manager_status: "future" }, { ...snapshot, recipient_user_id: source },
    { ...snapshot, expires_at: undefined }, { ...snapshot, projects: [] },
    { ...snapshot, projects: [{ ...snapshot.projects[0], reassign: "true" }] },
    { ...snapshot, projects: [{ ...snapshot.projects[0], version: "" }] },
    { ...snapshot, source_user_id: "not-a-uuid" },
  ])("rejects an unverifiable preview %j", async (body) => {
    respond(body);
    await expect(new ApiClient("").previewMemberHandover("ws", "member")).rejects.toThrow("could not be verified");
  });

  it("rejects a different receiver than the owner requested", async () => {
    respond(snapshot);
    await expect(new ApiClient("").previewMemberHandover("ws", "member", previewId)).rejects.toThrow("could not be verified");
  });

  it("sends only the saved preview ID when confirming", async () => {
    const fetch = respond({ ...snapshot, completed: true });
    await new ApiClient("").executeMemberHandover("ws", "member", previewId);
    expect(fetch).toHaveBeenCalledWith("/api/workspaces/ws/members/member/handover", expect.objectContaining({ method: "POST", body: JSON.stringify({ preview_id: previewId }) }));
  });

  it.each([null, {}, snapshot, { ...snapshot, completed: true, preview_id: recipient }, { ...snapshot, completed: "true" }])("rejects an unconfirmed or mismatched execution result %j", async (body) => {
    respond(body);
    await expect(new ApiClient("").executeMemberHandover("ws", "member", previewId)).rejects.toThrow("could not be verified");
  });

  it("allows a preview without an available manager but gives it no executable identity", async () => {
    const body = { ...snapshot, preview_id: "", recipient_user_id: "", manager_user_id: "", manager_status: "unlinked", expires_at: undefined };
    respond(body);
    expect((await new ApiClient("").previewMemberHandover("ws", "member")).preview_id).toBe("");
  });
});

describe("directory manager compatibility", () => {
  const member = { organization_id: "org", user_id: source };
  it("defaults missing and future manager statuses to an unknown relationship", () => {
    expect(ProjectAuthorizationOrganizationMemberSchema.parse(member)).toMatchObject({ manager_status: "not_synced", manager_sync_status: "not_synced" });
    expect(ProjectAuthorizationOrganizationMemberSchema.parse({ ...member, manager_status: "future", manager_sync_status: "future" }))
      .toMatchObject({ manager_status: "not_synced", manager_sync_status: "failed" });
  });
  it("keeps a previous relationship and records a failed refresh independently", () => {
    expect(ProjectAuthorizationOrganizationMemberSchema.parse({ ...member, manager_status: "ready", manager_name: "Leader", manager_sync_status: "failed" }))
      .toMatchObject({ manager_name: "Leader", manager_status: "ready", manager_sync_status: "failed" });
  });
});

describe("member handover status compatibility", () => {
  const member = { id: "member", workspace_id: "ws", user_id: source, role: "member" };

  it.each([undefined, null, "invalid", []])("preserves the member when optional diagnostics are malformed: %j", async (handover) => {
    respond([{ ...member, handover }]);
    const result = await new ApiClient("").listMembers("ws");
    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject(member);
    expect(result[0]?.handover).toBeUndefined();
  });

  it("does not treat unknown statuses or malformed fields as a successful handover", async () => {
    respond([{ ...member, handover: { status: "future", reason: 42, recipient_name: null, attempted_at: {}, notification_status: "future" } }]);
    expect((await new ApiClient("").listMembers("ws"))[0]?.handover).toEqual({
      status: "unknown", reason: "", recipient_name: "", attempted_at: null, notification_status: "",
    });
  });

  it.each(["pending", "staged", "delivered", "failed"])("retains the actual notification phase: %s", async (notificationStatus) => {
    respond([{ ...member, handover: { status: "completed", recipient_name: "Leader", notification_status: notificationStatus } }]);
    expect((await new ApiClient("").listMembers("ws"))[0]?.handover).toMatchObject({ status: "completed", recipient_name: "Leader", notification_status: notificationStatus });
  });
});
