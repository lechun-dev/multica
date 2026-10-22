import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import enSettings from "../../locales/en/settings.json";
import enCommon from "../../locales/en/common.json";

const mocks = vi.hoisted(() => ({ sync: vi.fn(), invalidate: vi.fn(), success: vi.fn(), warning: vi.fn(), error: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: { syncProjectAuthorizationDingTalk: mocks.sync } }));
vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: [{ user_id: "owner", role: "owner" }] }),
  useQueryClient: () => ({ invalidateQueries: mocks.invalidate }),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "w1" }));
vi.mock("@multica/core/paths", () => ({ useCurrentWorkspace: () => ({ name: "Lechun" }) }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: (select: (s: { user: { id: string } }) => unknown) => select({ user: { id: "owner" } }) }));
vi.mock("@multica/core/workspace/queries", () => ({ memberListOptions: () => ({}), workspaceKeys: { members: (ws: string) => ["members", ws] } }));
vi.mock("./project-authorization-directory", () => ({ ProjectAuthorizationDirectory: () => null }));
vi.mock("sonner", () => ({ toast: { success: mocks.success, warning: mocks.warning, error: mocks.error } }));
import { ProjectAuthorizationOrganizationsTab } from "./project-authorization-organizations-tab";

beforeEach(() => vi.clearAllMocks());
async function sync(status: string) {
  mocks.sync.mockResolvedValue({ organizations_created: 1, organizations_updated: 0, organizations_disabled: 0, members_created: 0, members_removed: 0, users_created: 0, users_matched: 0, workspace_members_created: 0, unmatched: [], departure_sync_status: status, accounts_disabled: status === "complete" ? 2 : 0, accounts_already_disabled: 1, departures_skipped: 3 });
  render(<I18nProvider locale="en" resources={{ en: { settings: enSettings, common: enCommon } }}><ProjectAuthorizationOrganizationsTab /></I18nProvider>);
  await userEvent.click(screen.getByRole("button", { name: "Sync DingTalk" }));
  expect(await screen.findByRole("dialog")).toHaveTextContent("accounts are never automatically re-enabled");
  await userEvent.click(screen.getByRole("button", { name: "Sync all from DingTalk" }));
  await waitFor(() => expect(mocks.sync).toHaveBeenCalledWith("w1"));
}

describe("DingTalk directory departure results", () => {
  it("shows confirmed suspension counts and invalidates only directory/member queries", async () => {
    await sync("complete");
    expect(await screen.findByText("Departed accounts disabled: 2")).toBeInTheDocument();
    expect(screen.getByText("Departure records skipped: 3 (active, unmatched or protected)")).toBeInTheDocument();
    expect(mocks.invalidate.mock.calls.map(([options]) => options.queryKey)).toEqual([["project-permission-organizations", "w1"], ["members", "w1"]]);
    expect(mocks.warning).not.toHaveBeenCalled();
  });
  it("makes failed departure verification visible instead of claiming a full success", async () => {
    await sync("unavailable");
    expect(await screen.findByRole("status")).toHaveTextContent("departure verification failed");
    expect(mocks.warning).toHaveBeenCalled();
    expect(mocks.success).not.toHaveBeenCalled();
  });
  it("explains owner-only protection when only the directory was synchronized", async () => {
    await sync("owner_required");
    expect(await screen.findByRole("status")).toHaveTextContent("requires a workspace owner");
  });
});
