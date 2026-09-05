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
      organizations: [
        { id: "org-a", workspace_id: "workspace-1", provider: "manual", external_id: "dept-a", name: "Department A", status: "active" },
        { id: "org-a-child", workspace_id: "workspace-1", provider: "manual", external_id: "dept-a-child", name: "Department A - Child", parent_id: "org-a", status: "active" },
      ],
      members: [], total: 1, member_total: 0,
    });
    listMembers.mockResolvedValue([
      { id: "membership-li", workspace_id: "workspace-1", user_id: "li-4", role: "member", created_at: "2026-09-03T00:00:00Z", name: "李四", email: "li4@example.com", avatar_url: null },
      { id: "membership-wang", workspace_id: "workspace-1", user_id: "wang-5", role: "member", created_at: "2026-09-03T00:00:00Z", name: "王五", email: "wang5@example.com", avatar_url: null },
    ]);
    createIssueAccessGrant.mockResolvedValue({ id: "grant-new" });
    revokeIssueAccessGrant.mockResolvedValue(undefined);
  });

  it("shows the grant audit columns and readable names for inherited grants", async () => {
    listIssueAccessGrants.mockResolvedValue({
      grants: [
        { id: "grant-user", issue_id: "project-1", workspace_id: "workspace-1", subject_type: "user", subject_id: "li-4", permission: "project.view", source: "project", created_at: "2026-09-03T10:00:00Z" },
        { id: "grant-org", issue_id: "project-1", workspace_id: "workspace-1", subject_type: "organization", subject_id: "org-a", role: "member", source: "project", created_at: "2026-09-03T11:00:00Z" },
      ],
      total: 2,
    });
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task permissions" }));
    const dialog = await screen.findByRole("dialog", { name: "Task permissions" });
    expect(within(dialog).getByRole("columnheader", { name: "Name" })).toBeInTheDocument();
    expect(within(dialog).getByRole("columnheader", { name: "Permission source" })).toBeInTheDocument();
    expect(within(dialog).getByRole("columnheader", { name: "Permission role" })).toBeInTheDocument();
    expect(within(dialog).getByRole("columnheader", { name: "Granted at" })).toBeInTheDocument();
    expect(within(dialog).getByText("李四")).toBeInTheDocument();
    expect(within(dialog).getByText("Department A")).toBeInTheDocument();
    expect(within(dialog).getAllByText("Inherited from project")).toHaveLength(2);
    const departmentRow = within(dialog).getByText("Department A").closest("tr");
    expect(departmentRow).not.toBeNull();
    expect(within(departmentRow as HTMLElement).getByText("Member")).toBeInTheDocument();
    expect(within(dialog).queryByText("li-4")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("org-a")).not.toBeInTheDocument();
  });

  it("shows a task permissions tooltip when hovering the icon", async () => {
    const user = userEvent.setup();
    renderDialog();
    const button = screen.getByRole("button", { name: "Task permissions" });

    await user.hover(button);

    expect(await screen.findByText("Task permissions", { selector: '[data-slot="tooltip-content"]' })).toBeInTheDocument();
  });

  it("submits multiple direct user role grants without a single-permission picker", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(screen.getByRole("button", { name: "Task permissions" }));
    const dialog = await screen.findByRole("dialog", { name: "Task permissions" });
    const subjectPicker = within(dialog).getByRole("combobox", { name: "Object type" });
    await user.click(subjectPicker);
    await user.click(await screen.findByRole("option", { name: "User" }));
    await user.click(within(dialog).getByRole("button", { name: "Select people" }));
    await user.click(await screen.findByRole("checkbox", { name: "李四" }));
    await user.click(await screen.findByRole("checkbox", { name: "王五" }));
    expect(screen.getAllByText("Selected people 2").length).toBeGreaterThan(0);
    expect(within(dialog).queryByLabelText("Grant type")).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText("Task permission")).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("combobox", { name: "Task role" }));
    const memberOption = await screen.findByRole("option", { name: "Member" });
    expect(memberOption.closest('[data-slot="select-content"]')).toHaveAttribute("data-align-trigger", "false");
    await user.click(memberOption);
    await user.click(within(dialog).getByRole("button", { name: "Add authorization" }));

    await waitFor(() => expect(createIssueAccessGrant).toHaveBeenCalledTimes(2));
    expect(createIssueAccessGrant).toHaveBeenCalledWith("issue-1", { subject_type: "user", subject_id: "li-4", role: "member" });
    expect(createIssueAccessGrant).toHaveBeenCalledWith("issue-1", { subject_type: "user", subject_id: "wang-5", role: "member" });
    expect(toastSuccess).toHaveBeenCalledWith("Task authorization added");
  });

  it("grants a task role directly to a department", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(screen.getByRole("button", { name: "Task permissions" }));
    const dialog = await screen.findByRole("dialog", { name: "Task permissions" });

    await user.click(within(dialog).getByRole("combobox", { name: "Object type" }));
    await user.click(await screen.findByRole("option", { name: "Organization" }));
    await user.click(within(dialog).getByRole("button", { name: "Select departments" }));
    const departmentA = await screen.findByRole("checkbox", { name: "Department A" });
    const departmentChild = await screen.findByRole("checkbox", { name: "Department A - Child" });
    await user.click(departmentA);
    await user.click(departmentChild);
    expect(screen.getAllByText("Selected departments 2").length).toBeGreaterThan(0);
    await user.click(within(dialog).getByRole("combobox", { name: "Task role" }));
    await user.click(await screen.findByRole("option", { name: "Member" }));
    await user.click(within(dialog).getByRole("button", { name: "Add authorization" }));

    await waitFor(() => expect(createIssueAccessGrant).toHaveBeenCalledTimes(2));
    expect(createIssueAccessGrant).toHaveBeenCalledWith("issue-1", { subject_type: "organization", subject_id: "org-a", role: "member" });
    expect(createIssueAccessGrant).toHaveBeenCalledWith("issue-1", { subject_type: "organization", subject_id: "org-a-child", role: "member" });
  });
});
