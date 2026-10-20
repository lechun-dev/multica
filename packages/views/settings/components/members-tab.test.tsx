import { render, screen, fireEvent } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import type { MemberWithUser } from "@multica/core/types";
import enSettings from "../../locales/en/settings.json";
import enCommon from "../../locales/en/common.json";
import enBilling from "../../locales/en/billing.json";

const state = vi.hoisted(() => ({ role: "owner", members: [] as MemberWithUser[] }));
const invalidate = vi.hoisted(() => vi.fn());
vi.mock("@tanstack/react-query", () => ({
 useQuery: (options: { queryKey: string[] }) => ({ data: options.queryKey[0] === "members" ? state.members : [] }),
 useQueryClient: () => ({ invalidateQueries: invalidate }),
}));
vi.mock("@multica/core/paths", () => ({ useCurrentWorkspace: () => ({ id:"w1",name:"Lechun",slug:"lechun" }) }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "w1" }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: (selector: (s: unknown) => unknown) => selector({ user: { id:"owner" } }) }));
vi.mock("@multica/core/billing", () => ({
 usePreviewWorkspaceSeatPurchase: () => ({ mutateAsync: vi.fn() }),
 usePurchaseWorkspaceSeats: () => ({ mutateAsync: vi.fn() }),
 workspaceSubscriptionSummaryOptions: () => ({ queryKey:["summary"] }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
 memberListOptions: () => ({ queryKey:["members"] }),
 invitationListOptions: () => ({ queryKey:["invitations"] }),
 shareLinkListOptions: () => ({ queryKey:["links"] }),
 workspaceKeys: { members: () => ["members"], invitations: () => ["invitations"] },
}));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("../../navigation", () => ({ useOptionalNavigation: () => null }));
vi.mock("@multica/core/api", () => ({ api: {}, errorCode: () => null }));
import { MembersTab } from "./members-tab";
const base: MemberWithUser = { id:"owner-member", user_id:"owner", workspace_id:"w1",role:"owner",name:"Owner",email:"owner@lechun.cc",avatar_url:null,created_at:"",has_logged_in:true };
function show() {
 state.members = [{ ...base, role: state.role as MemberWithUser["role"] },
  { ...base, id:"m1",user_id:"u1",role:"member",name:"张晓慧",email:"xiaohui@lechun.cc",can_manage_account:true },
  { ...base, id:"m2",user_id:"u2",role:"owner",name:"Other owner",email:"owner2@lechun.cc",can_manage_account:false },
 ];
 render(<I18nProvider locale="en" resources={{ en: { settings:enSettings,common:enCommon,billing:enBilling } }}><MembersTab /></I18nProvider>);
}
beforeEach(() => { state.role="owner"; vi.clearAllMocks(); });
describe("MembersTab search and account actions", () => {
 it("searches names and emails without removing the current owner's management authority", async () => {
  show(); const search = screen.getByRole("searchbox",{name:"Search by name or email"});
  fireEvent.change(search,{target:{value:"晓慧"}});
  expect(screen.getByText("张晓慧")).toBeInTheDocument();
  expect(screen.queryByText("Other owner")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button",{name:"Member actions"}));
  expect(await screen.findByRole("menuitem",{name:"Disable account"})).toBeInTheDocument();
  await userEvent.click(screen.getByRole("menuitem",{name:"Disable account"}));
  expect(await screen.findByRole("alertdialog")).toHaveTextContent("blocks sign-in across MissionOS");
 });
 it("never offers global account disable to an administrator", async () => {
  state.role="admin";show();
  fireEvent.change(screen.getByRole("searchbox"),{target:{value:"xiaohui@"}});
  await userEvent.click(screen.getByRole("button",{name:"Member actions"}));
  expect(screen.queryByRole("menuitem",{name:"Disable account"})).not.toBeInTheDocument();
 });
 it("does not offer account disable for another owner and shows an explicit empty search state", async () => {
  show(); const search=screen.getByRole("searchbox");
  fireEvent.change(search,{target:{value:"owner2@"}});
  await userEvent.click(screen.getByRole("button",{name:"Member actions"}));
  expect(screen.queryByRole("menuitem",{name:"Disable account"})).not.toBeInTheDocument();
  fireEvent.keyDown(document.body,{key:"Escape"});
  fireEvent.change(search,{target:{value:"no-such-member"}});
  expect(screen.getByText("No matching members")).toBeInTheDocument();
 });
});
