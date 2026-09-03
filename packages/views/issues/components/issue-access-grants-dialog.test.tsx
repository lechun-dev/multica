import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const {
  listIssueAccessGrants,
  listProjectPermissionRoles,
  listProjectAuthorizationOrganizations,
  listMembers,
  createIssueAccessGrant,
  revokeIssueAccessGrant,
  toastSuccess,
  toastError,
} = vi.hoisted(() => ({
  listIssueAccessGrants: vi.fn(),
  listProjectPermissionRoles: vi.fn(),
  listProjectAuthorizationOrganizations: vi.fn(),
  listMembers: vi.fn(),
  createIssueAccessGrant: vi.fn(),
  revokeIssueAccessGrant: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listIssueAccessGrants,
    listProjectPermissionRoles,
    listProjectAuthorizationOrganizations,
    listMembers,
    createIssueAccessGrant,
    revokeIssueAccessGrant,
  },
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("sonner", () => ({
  toast: {
    success: toastSuccess,
    error: toastError,
  },
}));

import { IssueAccessGrantsDialog } from "./issue-access-grants-dialog";

function renderDialog() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <IssueAccessGrantsDialog issueId="issue-1" projectId="project-1" />
    </QueryClientProvider>,
  );
}

describe("IssueAccessGrantsDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listIssueAccessGrants.mockResolvedValue({ grants: [], total: 0 });
    listProjectPermissionRoles.mockResolvedValue({ roles: [
      { id: "role-member", workspace_id: "workspace-1", key: "member", name: "Member", description: "", permissions: [], is_system: true },
      { id: "role-viewer", workspace_id: "workspace-1", key: "viewer", name: "Viewer", description: "", permissions: [], is_system: true },
    ] });
    listProjectAuthorizationOrganizations.mockResolvedValue({
      organizations: [{ id: "org-a", workspace_id: "workspace-1", provider: "manual", external_id: "dept-a", name: "Department A", status: "active" }],
      members: [], total: 1, member_total: 0,
    });
    listMembers.mockResolvedValue([
      { id: "membership-li", workspace_id: "workspace-1", user_id: "li-4", role: "member", created_at: "2026-09-03T00:00:00Z", name: "李四", email: "li4@example.com", avatar_url: null },
    ]);
    createIssueAccessGrant.mockResolvedValue({ id: "grant-new" });
    revokeIssueAccessGrant.mockResolvedValue(undefined);
  });

  it("shows readable names for inherited user and organization grants", async () => {
    listIssueAccessGrants.mockResolvedValue({
      grants: [
        { id: "grant-user", issue_id: "project-1", workspace_id: "workspace-1", subject_type: "user", subject_id: "li-4", permission: "project.view", source: "project" },
        { id: "grant-org", issue_id: "project-1", workspace_id: "workspace-1", subject_type: "organization", subject_id: "org-a", role: "member", source: "project" },
      ],
      total: 2,
    });
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task permissions" }));
    const dialog = await screen.findByRole("dialog", { name: "Task permissions" });
    expect(within(dialog).getByText("User: 李四")).toBeInTheDocument();
    expect(within(dialog).getByText("Organization: Department A")).toBeInTheDocument();
    expect(within(dialog).queryByText("li-4")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("org-a")).not.toBeInTheDocument();
  });

  it("uses the task-safe permission list and submits a direct user grant", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(screen.getByRole("button", { name: "Task permissions" }));
    const dialog = await screen.findByRole("dialog", { name: "Task permissions" });
    const comboboxes = within(dialog).getAllByRole("combobox");

    await user.click(comboboxes[1]!);
    await user.click(await screen.findByRole("option", { name: "李四" }));
    const permissionPicker = within(dialog).getByRole("combobox", { name: "Task permission" });
    await user.click(permissionPicker);
    expect(screen.queryByRole("option", { name: "Create related tasks" })).not.toBeInTheDocument();
    await user.click(await screen.findByRole("option", { name: "Comment on task" }));
    await user.click(within(dialog).getByRole("button", { name: "Add authorization" }));

    await waitFor(() => expect(createIssueAccessGrant).toHaveBeenCalledWith("issue-1", {
      subject_type: "user",
      subject_id: "li-4",
      permission: "project.issue.comment",
    }));
    expect(toastSuccess).toHaveBeenCalledWith("Task authorization added");
  });

  it("grants a task role directly to a department", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(screen.getByRole("button", { name: "Task permissions" }));
    const dialog = await screen.findByRole("dialog", { name: "Task permissions" });

    await user.click(within(dialog).getByRole("combobox", { name: "Authorization subject" }));
    await user.click(await screen.findByRole("option", { name: "Organization" }));
    await user.click(within(dialog).getByRole("combobox", { name: "Organization" }));
    await user.click(await screen.findByRole("option", { name: /Department A/ }));
    await user.click(within(dialog).getByRole("combobox", { name: "Grant type" }));
    await user.click(await screen.findByRole("option", { name: "Role permissions" }));
    await user.click(within(dialog).getByRole("button", { name: "Add authorization" }));

    await waitFor(() => expect(createIssueAccessGrant).toHaveBeenCalledWith("issue-1", {
      subject_type: "organization",
      subject_id: "org-a",
      role: "member",
    }));
  });
});
