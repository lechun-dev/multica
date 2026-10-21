import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@multica/core/api";
import { configStore } from "@multica/core/config";
import { renderWithI18n } from "../../test/i18n";

const mocks = vi.hoisted(() => ({
  getIssueAccessControl: vi.fn(),
  revokeIssueMentionAccess: vi.fn(),
  getIssueEffectiveAccess: vi.fn(),
  listTaskPermissionRoles: vi.fn(),
  listProjectAuthorizationOrganizations: vi.fn(),
  listMembers: vi.fn(),
  listIssueAccessRequests: vi.fn(),
  updateIssueAccessControl: vi.fn(),
  reviewIssueAccessRequest: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: mocks,
  // Same shape as the real ApiError: the dialog branches on `status`, so a mock
  // that put the message there would quietly take the wrong branch.
  ApiError: class ApiError extends Error {
    status: number;
    constructor(message: string, status: number, statusText?: string) {
      super(message);
      this.status = status;
      this.name = statusText || "ApiError";
    }
  },
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("sonner", () => ({
  toast: { success: mocks.toastSuccess, error: mocks.toastError },
}));

import { IssueAccessGrantsDialog } from "./issue-access-grants-dialog";

const control = {
  workspace_id: "workspace-1",
  issue_id: "issue-1",
  project_id: "project-1",
  scope: "task" as const,
  project_access_mode: "inherit" as const,
  policy_version: 4,
  grants: [],
};

function renderDialog(
  projectId: string | null = "project-1",
  focus: { focusRequestId?: string | null; focusRequestToken?: number } = {},
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <IssueAccessGrantsDialog
        issueId="issue-1"
        projectId={projectId}
        focusRequestId={focus.focusRequestId ?? null}
        focusRequestToken={focus.focusRequestToken}
      />
    </QueryClientProvider>,
  );
}

const pendingRequest = {
  id: "request-9",
  workspace_id: "workspace-1",
  issue_id: "issue-1",
  requester_user_id: "li-4",
  requested_role: "viewer",
  reason: "Need task context",
  status: "pending" as const,
  created_at: "2026-09-16T00:00:00Z",
  updated_at: "2026-09-16T00:00:00Z",
};

const workspaceMember = {
  id: "membership-li",
  workspace_id: "workspace-1",
  user_id: "li-4",
  role: "member",
  created_at: "2026-09-03T00:00:00Z",
  name: "李四",
  email: "li4@example.com",
  avatar_url: null,
  has_logged_in: true,
};

describe("IssueAccessGrantsDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    configStore.getState().setAuthConfig({
      allowSignup: true,
      projectPermissionsEnabled: true,
      projectPermissionRolloutPhase: "restricted",
    });
    mocks.getIssueAccessControl.mockResolvedValue(control);
    mocks.getIssueEffectiveAccess.mockResolvedValue({
      ...control,
      permissions: ["project.view"],
      sources: [
        {
          permission: "project.view",
          source: "project_direct",
          role: "viewer",
          scope: "project",
          source_resource: { scope: "project", id: "project-1" },
          target_resource: { scope: "task", id: "issue-1" },
          policy_version: 4,
        },
      ],
    });
    mocks.listTaskPermissionRoles.mockResolvedValue({
      scope: "task",
      roles: [
        {
          id: "member",
          workspace_id: "workspace-1",
          key: "member",
          name: "Member",
          description: "",
          permissions: ["project.view", "project.edit"],
          is_system: true,
          scope: "task",
        },
        {
          id: "viewer",
          workspace_id: "workspace-1",
          key: "viewer",
          name: "Viewer",
          description: "",
          permissions: ["project.view"],
          is_system: true,
          scope: "task",
        },
      ],
    });
    mocks.listProjectAuthorizationOrganizations.mockResolvedValue({
      organizations: [
        {
          id: "org-sales",
          workspace_id: "workspace-1",
          name: "销售部",
          external_id: "dept-sales",
          parent_id: null,
          sort_order: 0,
          created_at: "2026-09-03T00:00:00Z",
          updated_at: "2026-09-03T00:00:00Z",
        },
      ],
      members: [],
      total: 1,
      member_total: 0,
    });
    mocks.listMembers.mockResolvedValue([workspaceMember]);
    mocks.listIssueAccessRequests.mockResolvedValue({ items: [] });
    mocks.updateIssueAccessControl.mockResolvedValue(control);
  });

  it("leads with sharing and keeps other people's access behind the list", async () => {
    const user = userEvent.setup();
    renderDialog();

    const trigger = screen.getByRole("button", { name: "Task access" });
    expect(trigger).toHaveTextContent("Task access");
    await user.click(trigger);

    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    expect(within(dialog).getByText("Task link")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Copy link" })).toBeInTheDocument();
    expect(within(dialog).getByText("Grant access")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Already granted 0" })).toBeInTheDocument();
    expect(within(dialog).queryByRole("group", { name: "Object type" })).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Select people" })).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Select departments" })).toBeInTheDocument();
    expect(within(dialog).getByRole("checkbox", { name: /Everyone/ })).toBeInTheDocument();
    expect(within(dialog).getByText("Grant settings")).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Add to access list" })).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Save" })).toBeDisabled();
    expect(within(dialog).getByText("My access")).toBeInTheDocument();
    expect(within(dialog).getAllByText("View task").length).toBeGreaterThan(0);
    expect(within(dialog).queryByText("Allowed")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("Permission source details")).not.toBeInTheDocument();
    expect(within(dialog).queryByTestId("derived-access-row")).not.toBeInTheDocument();
  });

  it("saves a projectless task grant directly", async () => {
    const user = userEvent.setup();
    renderDialog(null);

    await user.click(screen.getByRole("button", { name: "Task access" }));

    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    expect(within(dialog).getByText(/Projectless tasks use direct grants only/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Select people" }));
    await user.click(await screen.findByRole("checkbox", { name: "李四" }));
    await user.click(within(dialog).getByRole("combobox", { name: "Access level" }));
    await user.click(await screen.findByRole("option", { name: "Can edit" }));
    await user.type(within(dialog).getByLabelText("Grant expiry"), "2026-09-20T12:00");

    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    const expectedUpdate = expect.objectContaining({
      expected_version: 4,
      grants: [
        expect.objectContaining({
          subject_id: "li-4",
          role: "member",
          scope: "task",
          expires_at: expect.any(String),
        }),
      ],
    });
    await waitFor(() => expect(mocks.updateIssueAccessControl).toHaveBeenCalledWith("issue-1", expectedUpdate));
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Task permissions updated");
  });

  it("disables people and department pickers while saving everyone access", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task access" }));

    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    await user.click(within(dialog).getByRole("button", { name: "Select people" }));
    await user.click(await screen.findByRole("checkbox", { name: "李四" }));
    await user.click(within(dialog).getByRole("button", { name: "Select departments" }));
    await user.click(await screen.findByRole("checkbox", { name: "销售部" }));
    await user.click(within(dialog).getByRole("checkbox", { name: /Everyone/ }));

    expect(within(dialog).getByRole("button", { name: "Select people" })).toHaveAttribute("aria-disabled", "true");
    expect(within(dialog).getByRole("button", { name: "Select departments" })).toHaveAttribute("aria-disabled", "true");
    expect(within(dialog).queryByRole("button", { name: "Add to access list" })).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(mocks.updateIssueAccessControl).toHaveBeenCalledWith(
      "issue-1",
      expect.objectContaining({
        grants: [expect.objectContaining({ subject_type: "everyone", role: "member" })],
      }),
    ));
  });

  it("resolves each applicant by user ID rather than request or membership ID", async () => {
    mocks.listMembers.mockResolvedValue([
      workspaceMember,
      { ...workspaceMember, id: "membership-wang", user_id: "wang-5", name: "王五" },
    ]);
    mocks.listIssueAccessRequests.mockResolvedValue({ items: [
      pendingRequest,
      { ...pendingRequest, id: "request-10", requester_user_id: "wang-5", reason: "Second applicant" },
    ] });
    renderDialog("project-1", { focusRequestId: pendingRequest.id });
    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    for (const { reason, name, userId } of [
      { reason: pendingRequest.reason, name: "李四", userId: "li-4" },
      { reason: "Second applicant", name: "王五", userId: "wang-5" },
    ]) {
      const row = (await within(dialog).findByText(reason)).closest("[data-request-id]");
      await waitFor(() => expect(row).toHaveTextContent(name));
      expect(row).not.toHaveTextContent(userId);
    }
  });

  it.each([
    { name: "  ", email: "li4@example.com", expected: "li4@example.com" },
    { name: "", email: "", expected: "User" },
  ])("uses readable fallback $expected when the applicant has no name", async ({ name, email, expected }) => {
    mocks.listMembers.mockResolvedValue([{ ...workspaceMember, name, email }]);
    mocks.listIssueAccessRequests.mockResolvedValue({ items: [pendingRequest] });
    renderDialog("project-1", { focusRequestId: pendingRequest.id });
    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    const row = (await within(dialog).findByText(pendingRequest.reason)).closest("[data-request-id]");
    await waitFor(() => expect(row).toHaveTextContent(expected));
    expect(row).not.toHaveTextContent(pendingRequest.requester_user_id);
  });

  it("never shows an applicant ID while the member directory loads", async () => {
    let resolveMembers!: (members: typeof workspaceMember[]) => void;
    mocks.listMembers.mockReturnValue(new Promise<typeof workspaceMember[]>((resolve) => { resolveMembers = resolve; }));
    mocks.listIssueAccessRequests.mockResolvedValue({ items: [pendingRequest] });
    renderDialog("project-1", { focusRequestId: pendingRequest.id });
    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    const row = (await within(dialog).findByText(pendingRequest.reason)).closest("[data-request-id]");
    expect(row).toHaveTextContent("Loading…");
    expect(row).not.toHaveTextContent(pendingRequest.requester_user_id);
    await act(async () => resolveMembers([workspaceMember]));
    await waitFor(() => expect(row).toHaveTextContent("李四"));
  });

  it.each(["missing", "failed"])("does not expose applicant IDs when directory lookup is %s", async (state) => {
    if (state === "failed") mocks.listMembers.mockRejectedValue(new Error("Directory unavailable"));
    else mocks.listMembers.mockResolvedValue([]);
    mocks.listIssueAccessRequests.mockResolvedValue({ items: [pendingRequest] });
    renderDialog("project-1", { focusRequestId: pendingRequest.id });
    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    const row = (await within(dialog).findByText(pendingRequest.reason)).closest("[data-request-id]");
    await waitFor(() => expect(row).toHaveTextContent(state === "failed" ? "Failed to load workspace members" : "User"));
    expect(row).not.toHaveTextContent(pendingRequest.requester_user_id);
  });

  it("moves an approved access request into direct grants", async () => {
    const pendingRequest = {
      id: "request-1",
      workspace_id: "workspace-1",
      issue_id: "issue-1",
      requester_user_id: "li-4",
      requested_role: "viewer",
      reason: "Need task context",
      status: "pending" as const,
      created_at: "2026-09-16T00:00:00Z",
      updated_at: "2026-09-16T00:00:00Z",
    };
    mocks.listIssueAccessRequests
      .mockResolvedValueOnce({ items: [pendingRequest] })
      .mockResolvedValue({ items: [{ ...pendingRequest, status: "approved" }] });
    mocks.reviewIssueAccessRequest.mockResolvedValue({
      ...pendingRequest,
      status: "approved",
      reviewer_user_id: "reviewer-1",
      reviewed_at: "2026-09-16T00:01:00Z",
      updated_at: "2026-09-16T00:01:00Z",
    });

    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task access" }));

    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    const requestRow = (await within(dialog).findByText("Need task context")).closest("[data-request-id]");
    expect(requestRow).not.toBeNull();
    await waitFor(() => expect(requestRow).toHaveTextContent("李四"));
    expect(requestRow).not.toHaveTextContent("li-4");
    await user.click(await within(dialog).findByRole("button", { name: "Approve" }));

    await waitFor(() => expect(mocks.reviewIssueAccessRequest).toHaveBeenCalledWith(
      "issue-1",
      "request-1",
      { action: "approve" },
    ));
    expect(await within(dialog).findByRole("button", { name: "Already granted 1" })).toBeInTheDocument();
    expect(within(dialog).getByText("Need task context")).toBeInTheDocument();
    expect(await within(dialog).findByText("This request was already handled: Approved.")).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Already granted 1" }));
    const grantsDialog = await screen.findByRole("dialog", { name: "Direct task access" });
    expect(within(grantsDialog).getByText("李四")).toBeInTheDocument();
    expect(within(grantsDialog).getByText("Can view")).toBeInTheDocument();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Access request approved");
  });

  it("counts and lists access that a mention granted, without offering to remove it", async () => {
    // A mention stores a real grant outside the manual ACL. Counting only the
    // manual rows reported "Already granted 0" while the mentioned teammate could
    // plainly open the task, which reads as "nobody has access".
    mocks.getIssueAccessControl.mockResolvedValue({
      ...control,
      derived_grants: [
        { subject_type: "user" as const, subject_id: "user-9", role: "member", source: "system", reason: "mention" },
        { subject_type: "user" as const, subject_id: "user-1", role: "owner", source: "system", reason: "creator" },
      ],
    } as never);
    mocks.revokeIssueMentionAccess.mockResolvedValue({ ...control, derived_grants: [] });

    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task access" }));
    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    await user.click(await within(dialog).findByRole("button", { name: "Already granted 2" }));

    const grantsDialog = await screen.findByRole("dialog", { name: "Direct task access" });
    const rows = within(grantsDialog).getAllByTestId("derived-access-row");
    const mentionRow = rows.find((item) => item.textContent?.includes("@mention"));
    const creatorRow = rows.find((item) => item.textContent?.includes("Task creator"));
    if (!mentionRow || !creatorRow) {
      throw new Error("expected both the mention and the creator row");
    }
    // A mistaken mention has to be withdrawable, and the server remembers the
    // decision so the next reconciliation cannot hand the access back.
    await user.click(within(mentionRow).getByRole("button"));
    await waitFor(() => expect(mocks.revokeIssueMentionAccess).toHaveBeenCalledWith("issue-1", "user-9"));
    // The creator owns the task by definition; only a mention can be withdrawn.
    expect(within(creatorRow).queryByRole("button")).not.toBeInTheDocument();
  });

  it("names the source of each permission separately", async () => {
    // One merged source line cannot answer "why do I have this permission" once
    // two of them arrive from different places.
    mocks.getIssueEffectiveAccess.mockResolvedValue({
      ...control,
      permissions: ["project.view", "project.issue.manage"],
      sources: [
        { permission: "project.view", source: "creator", role: "owner", scope: "task", source_resource: { scope: "task", id: "issue-1" }, target_resource: { scope: "task", id: "issue-1" }, policy_version: 4 },
        { permission: "project.issue.manage", source: "project_direct", role: "manager", scope: "project", source_resource: { scope: "project", id: "project-1" }, target_resource: { scope: "task", id: "issue-1" }, policy_version: 4 },
      ],
    });

    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task access" }));
    const dialog = await screen.findByRole("dialog", { name: "Share task" });

    const view = await within(dialog).findByText("View task");
    expect(view.nextElementSibling).toHaveTextContent("Task creator");
    const manage = within(dialog).getByText("Manage task");
    expect(manage.nextElementSibling).toHaveTextContent("Project direct grant");
    // Each row carries only its own source, grouped under its own layer.
    expect(manage.nextElementSibling).not.toHaveTextContent("Task creator");
    expect(within(dialog).getByText("This task")).toBeInTheDocument();
    expect(within(dialog).getByText("From project")).toBeInTheDocument();
  });

  it("removes a manual grant immediately, without the other dialog's save", async () => {
    // The list says it manages manual access, so the removal has to happen here:
    // it used to only mark the dialog dirty, and closing the list discarded it.
    const existingGrant = { subject_type: "user" as const, subject_id: "user-9", role: "manager", scope: "task" as const };
    mocks.getIssueAccessControl.mockResolvedValue({ ...control, grants: [existingGrant] } as never);
    mocks.updateIssueAccessControl.mockResolvedValue({ ...control, grants: [] } as never);

    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task access" }));
    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    await user.click(await within(dialog).findByRole("button", { name: "Already granted 1" }));

    const grantsDialog = await screen.findByRole("dialog", { name: "Direct task access" });
    await user.click(within(within(grantsDialog).getByTestId("manual-access-row")).getByRole("button"));

    await waitFor(() => expect(mocks.updateIssueAccessControl).toHaveBeenCalledWith(
      "issue-1",
      expect.objectContaining({ grants: [] }),
    ));
  });

  it("keeps explanation read-only when the caller has no Manage permission", async () => {
    // The server refuses with 403. A generic failure must NOT read as a permission
    // decision — that case is covered separately below.
    mocks.getIssueAccessControl.mockRejectedValue(new ApiError("forbidden", 403, "Forbidden"));
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task access" }));

    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    expect(
      within(dialog).getByText(/only someone with Manage task permission/),
    ).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
    // The reader sees where their own permissions come from, not just which ones
    // they hold — the mocked explanation grants view through a project grant.
    expect(within(dialog).getByText(/Project direct grant/)).toBeInTheDocument();
  });

  it("reports a failed request as a failure, not as a missing permission", async () => {
    // A 5xx or a network error used to render the same "only a manager can change
    // sharing" panel, so a broken request pointed the reader at the wrong cause.
    mocks.getIssueAccessControl.mockRejectedValue(new ApiError("unavailable", 503, "Service Unavailable"));
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task access" }));

    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    expect(within(dialog).getByText(/Could not load this task's access settings/)).toBeInTheDocument();
    expect(within(dialog).queryByText(/only someone with Manage task permission/)).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("allows a task manager to change access in any active rollout phase", async () => {
    configStore.getState().setAuthConfig({
      allowSignup: true,
      projectPermissionsEnabled: true,
      projectPermissionRolloutPhase: "reader",
    });
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task access" }));

    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    expect(within(dialog).getByText("My access")).toBeInTheDocument();
    expect(within(dialog).getByText("Grant access")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Save" })).toBeInTheDocument();
  });

  // 2026-10-20 coder(lq): Opening a request focuses review without approving it.
  it("opens on its own and lands on the request the notification pointed at", async () => {
    mocks.listIssueAccessRequests.mockResolvedValue({ items: [pendingRequest] });

    renderDialog("project-1", { focusRequestId: pendingRequest.id });

    // No trigger click: the focus request alone has to open the dialog.
    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    const row = await within(dialog).findByText(pendingRequest.reason);
    expect(row.closest("[data-request-id]")).toHaveAttribute(
      "data-request-id",
      pendingRequest.id,
    );
    expect(within(dialog).getByText(/Focused on this access request/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Reject" })).toBeInTheDocument();
    expect(mocks.reviewIssueAccessRequest).not.toHaveBeenCalled();
  });

  it.each([null, "request-9", "request-10"])("keeps both handled applications when focused request is %s", async (focusRequestId) => {
    mocks.listMembers.mockResolvedValue([
      workspaceMember,
      { ...workspaceMember, id: "membership-wang", user_id: "wang-5", name: "王五" },
    ]);
    mocks.listIssueAccessRequests.mockResolvedValue({ items: [
      { ...pendingRequest, id: "request-10", requester_user_id: "wang-5", reason: "Second application", status: "rejected" },
      { ...pendingRequest, status: "approved" },
    ] });
    const user = userEvent.setup();
    renderDialog("project-1", { focusRequestId });
    if (!focusRequestId) await user.click(screen.getByRole("button", { name: "Task access" }));
    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    expect(await within(dialog).findByText("Access requests")).toBeInTheDocument();
    const approvedRow = (await within(dialog).findByText(pendingRequest.reason)).closest("[data-request-id]");
    const rejectedRow = (await within(dialog).findByText("Second application")).closest("[data-request-id]");
    await waitFor(() => expect(approvedRow).toHaveTextContent("李四"));
    await waitFor(() => expect(rejectedRow).toHaveTextContent("王五"));
    expect(approvedRow).toHaveTextContent("This request was already handled: Approved.");
    expect(rejectedRow).toHaveTextContent("This request was already handled: Rejected.");
    expect(dialog.querySelectorAll("[data-request-id]")).toHaveLength(2);
    expect(within(dialog).queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Reject" })).not.toBeInTheDocument();
    expect(mocks.reviewIssueAccessRequest).not.toHaveBeenCalled();
  });

  it("keeps earlier requests from the same applicant and preserves both records after rejection", async () => {
    const earlierRequest = { ...pendingRequest, id: "earlier-request", reason: "Earlier application", status: "approved" };
    const rejectedRequest = { ...pendingRequest, status: "rejected" };
    mocks.listIssueAccessRequests
      .mockResolvedValueOnce({ items: [pendingRequest, earlierRequest] })
      .mockResolvedValue({ items: [rejectedRequest, earlierRequest] });
    mocks.reviewIssueAccessRequest.mockResolvedValue(rejectedRequest);
    const user = userEvent.setup();
    renderDialog("project-1", { focusRequestId: pendingRequest.id });
    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    expect(await within(dialog).findByText("Earlier application")).toBeInTheDocument();
    expect(within(dialog).getByText("Access requests")).toBeInTheDocument();
    expect(dialog.querySelectorAll("[data-request-id]")).toHaveLength(2);
    expect(within(dialog).getAllByRole("button", { name: "Approve" })).toHaveLength(1);
    await user.click(within(dialog).getByRole("button", { name: "Reject" }));
    await waitFor(() => expect(mocks.reviewIssueAccessRequest).toHaveBeenCalledWith("issue-1", pendingRequest.id, { action: "reject" }));
    expect(await within(dialog).findByText("This request was already handled: Rejected.")).toBeInTheDocument();
    expect(within(dialog).getByText("This request was already handled: Approved.")).toBeInTheDocument();
    expect(within(dialog).getByText(pendingRequest.reason)).toBeInTheDocument();
    expect(within(dialog).getByText("Earlier application")).toBeInTheDocument();
    expect(dialog.querySelectorAll("[data-request-id]")).toHaveLength(2);
    expect(within(dialog).queryByRole("button", { name: "Reject" })).not.toBeInTheDocument();
  });

  it("shows a decided request as handled instead of re-offering the decision", async () => {
    // A notification outlives the decision it announces. Landing on it must
    // answer "what happened?" — and never put Approve/Reject back on screen.
    mocks.listIssueAccessRequests.mockResolvedValue({
      items: [{ ...pendingRequest, status: "approved" as const }],
    });

    renderDialog("project-1", { focusRequestId: pendingRequest.id });

    const dialog = await screen.findByRole("dialog", { name: "Share task" });
    expect(
      await within(dialog).findByText("This request was already handled: Approved."),
    ).toBeInTheDocument();
    const row = within(dialog).getByText(pendingRequest.reason).closest("[data-request-id]");
    await waitFor(() => expect(row).toHaveTextContent("李四"));
    expect(row).not.toHaveTextContent(pendingRequest.requester_user_id);
    expect(within(dialog).queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Reject" })).not.toBeInTheDocument();
  });
});
