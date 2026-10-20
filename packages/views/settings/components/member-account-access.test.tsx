import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { MemberWithUser } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";
import { filterMembers, MemberAccountAccessDialog, MemberSearchControls } from "./member-account-access";

const setAccess = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/api", () => ({ api: { setMemberAccountAccess: setAccess } }));
const member: MemberWithUser = { id: "m1", user_id: "u1", workspace_id: "w1", role: "member", name: "张晓慧", email: "Xiaohui@lechun.cc", created_at: "", avatar_url: null };
const members = [member, { ...member, id: "m2", name: "李会", email: "lihui@lechun.cc", account_disabled: true }];
function wrap(children: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<I18nProvider locale="en" resources={{ en: { common: enCommon, settings: enSettings } }}>
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  </I18nProvider>);
}
afterEach(() => vi.clearAllMocks());

describe("member search and account access", () => {
  it("filters names, case-insensitive emails, whitespace and account status without mutating the source", () => {
    expect(filterMembers(members, "晓慧", "all")).toEqual([member]);
    expect(filterMembers(members, " XIAOHUI@ ", "enabled")).toEqual([member]);
    expect(filterMembers(members, "", "disabled")).toEqual([members[1]]);
    expect(filterMembers(members, "晓慧", "disabled")).toEqual([]);
    expect(filterMembers(members, "unknown", "all")).toEqual([]);
    expect(filterMembers(members, "", "all")).toEqual(members);
    expect(members).toHaveLength(2);
  });
  it("labels the search input and reports edits", () => {
    const change = vi.fn();
    wrap(<MemberSearchControls search="" status="all" onSearchChange={change} onStatusChange={vi.fn()} />);
    fireEvent.change(screen.getByRole("searchbox", { name: "Search by name or email" }), { target: { value: "晓慧" } });
    expect(change).toHaveBeenCalledWith("晓慧");
    expect(screen.getByRole("combobox", { name: "Account status" })).toBeInTheDocument();
  });
  it("does not mutate the account until the global-impact confirmation is accepted", async () => {
    const close = vi.fn(); setAccess.mockResolvedValue({ ...member, account_disabled: true });
    wrap(<MemberAccountAccessDialog workspaceId="w1" member={member} onClose={close} />);
    expect(screen.getByText(/blocks sign-in across MissionOS/)).toBeInTheDocument();
    expect(setAccess).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Disable account" }));
    await waitFor(() => expect(close).toHaveBeenCalledTimes(1));
    expect(setAccess).toHaveBeenCalledWith("w1", "m1", true);
  });
  it("preserves the dialog on failure and supports retry", async () => {
    const close = vi.fn(); setAccess.mockRejectedValueOnce(new Error("denied")).mockResolvedValueOnce(member);
    wrap(<MemberAccountAccessDialog workspaceId="w1" member={member} onClose={close} />);
    fireEvent.click(screen.getByRole("button", { name: "Disable account" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not update account access");
    expect(close).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Disable account" }));
    await waitFor(() => expect(close).toHaveBeenCalledTimes(1));
  });
  it("re-enables without claiming that old credentials are restored", async () => {
    setAccess.mockResolvedValue(member); const close = vi.fn();
    wrap(<MemberAccountAccessDialog workspaceId="w1" member={{ ...member, account_disabled: true }} onClose={close} />);
    expect(screen.getByText(/Previous sessions and revoked API tokens are not restored/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Enable account" }));
    await waitFor(() => expect(setAccess).toHaveBeenCalledWith("w1", "m1", false));
  });
});
