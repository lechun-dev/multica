import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MemberHandoverPreview } from "@multica/core/api";
import type { MemberWithUser } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { MemberHandoverDialog } from "./member-handover";

const { preview, execute } = vi.hoisted(() => ({ preview: vi.fn(), execute: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: { previewMemberHandover: preview, executeMemberHandover: execute } }));
const member: MemberWithUser = { id: "member", user_id: "source", workspace_id: "ws", role: "member", name: "Departed", email: "source@example.test", created_at: "", avatar_url: null, account_disabled: true };
const leader = { ...member, id: "leader-member", user_id: "leader", name: "Leader", account_disabled: false };
const other = { ...leader, id: "other-member", user_id: "other", name: "Other receiver" };
function snapshot(over: Partial<MemberHandoverPreview> = {}): MemberHandoverPreview {
  return {
    preview_id: "preview", source_user_id: "source", recipient_user_id: "leader",
    manager_user_id: "leader", manager_name: "Leader", manager_status: "ready", manager_sync_status: "complete",
    projects: [{ id: "project", project_id: "", title: "Unfinished project", role: "owner", reassign: false, version: "v1" }], issues: [],
    expires_at: new Date(Date.now() + 900_000).toISOString(), completed: false, ...over,
  };
}
function renderDialog() {
  const close = vi.fn();
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  renderWithI18n(<QueryClientProvider client={qc}><MemberHandoverDialog workspaceId="ws" member={member} members={[member, leader, other]} onClose={close} /></QueryClientProvider>);
  return { close, qc };
}
beforeEach(() => { preview.mockResolvedValue(snapshot()); execute.mockResolvedValue(snapshot({ completed: true })); });
afterEach(() => vi.resetAllMocks());

describe("member handover confirmation", () => {
  it("loads a preview and waits for explicit confirmation", async () => {
    const { close } = renderDialog();
    expect(await screen.findByText("Unfinished project")).toBeInTheDocument();
    expect(preview).toHaveBeenCalledWith("ws", "member", undefined);
    expect(execute).not.toHaveBeenCalled();
    expect(screen.getByRole("combobox", { name: "Recipient" })).toHaveTextContent("Leader");
    fireEvent.click(screen.getByRole("button", { name: "Open permissions" }));
    await waitFor(() => expect(close).toHaveBeenCalledOnce());
    expect(execute).toHaveBeenCalledWith("ws", "member", "preview");
  });

  it("invalidates the old preview when changing the recipient", async () => {
    const user = userEvent.setup();
    renderDialog();
    await screen.findByText("Unfinished project");
    await user.click(screen.getByRole("combobox", { name: "Recipient" }));
    await user.click(await screen.findByRole("option", { name: "Other receiver" }));
    expect(screen.getByRole("button", { name: "Open permissions" })).toBeDisabled();
    expect(execute).not.toHaveBeenCalled();
    preview.mockResolvedValueOnce(snapshot({ preview_id: "other-preview", recipient_user_id: "other" }));
    fireEvent.click(screen.getByRole("button", { name: "Refresh preview" }));
    await waitFor(() => expect(preview).toHaveBeenLastCalledWith("ws", "member", "other"));
    await waitFor(() => expect(screen.getByRole("button", { name: "Open permissions" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Open permissions" }));
    await waitFor(() => expect(execute).toHaveBeenCalledWith("ws", "member", "other-preview"));
  });

  it("keeps the dialog on failure and requires a new preview", async () => {
    execute.mockRejectedValueOnce(new Error("409 changed"));
    const { close } = renderDialog();
    await screen.findByText("Unfinished project");
    fireEvent.click(screen.getByRole("button", { name: "Open permissions" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not open permissions");
    expect(close).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Open permissions" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Refresh preview" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Open permissions" })).toBeEnabled());
  });

  it("blocks cancellation while submitting", async () => {
    let resolve!: (value: MemberHandoverPreview) => void;
    execute.mockImplementationOnce(() => new Promise<MemberHandoverPreview>((done) => { resolve = done; }));
    const { close } = renderDialog();
    await screen.findByText("Unfinished project");
    fireEvent.click(screen.getByRole("button", { name: "Open permissions" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled());
    fireEvent.keyDown(screen.getByRole("alertdialog"), { key: "Escape" });
    expect(close).not.toHaveBeenCalled();
    resolve(snapshot({ completed: true }));
    await waitFor(() => expect(close).toHaveBeenCalledOnce());
  });

  it.each([
    { preview_id: "", recipient_user_id: "", manager_user_id: "", manager_status: "unlinked" as const, expires_at: undefined },
    { preview_id: "", projects: [], expires_at: undefined },
    { expires_at: "2020-01-01T00:00:00Z" },
  ])("does not confirm an unavailable or expired preview %j", async (over) => {
    preview.mockResolvedValueOnce(snapshot(over));
    renderDialog();
    await waitFor(() => expect(preview).toHaveBeenCalledOnce());
    await waitFor(() => expect(screen.queryByRole("status")).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Open permissions" })).toBeDisabled();
    expect(execute).not.toHaveBeenCalled();
  });

  it("rejects a preview for a different source member", async () => {
    preview.mockResolvedValueOnce(snapshot({ source_user_id: "someone-else" }));
    renderDialog();
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not prepare the preview");
    expect(screen.getByRole("button", { name: "Open permissions" })).toBeDisabled();
  });
});
