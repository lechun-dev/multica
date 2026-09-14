import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { configStore } from "@multica/core/config";
import { renderWithI18n } from "../../test/i18n";

const mocks = vi.hoisted(() => ({
  getIssueAccessControl: vi.fn(),
  getIssueEffectiveAccess: vi.fn(),
  listTaskPermissionRoles: vi.fn(),
  listProjectAuthorizationOrganizations: vi.fn(),
  listMembers: vi.fn(),
  listIssueAccessRequests: vi.fn(),
  previewIssueAccessControl: vi.fn(),
  updateIssueAccessControl: vi.fn(),
  reviewIssueAccessRequest: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: mocks,
  ApiError: class ApiError extends Error {
    status: number;
    constructor(status: number) {
      super();
      this.status = status;
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

function renderDialog(projectId: string | null = "project-1") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <IssueAccessGrantsDialog issueId="issue-1" projectId={projectId} />
    </QueryClientProvider>,
  );
}

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
      organizations: [],
      members: [],
      total: 0,
      member_total: 0,
    });
    mocks.listMembers.mockResolvedValue([
      {
        id: "membership-li",
        workspace_id: "workspace-1",
        user_id: "li-4",
        role: "member",
        created_at: "2026-09-03T00:00:00Z",
        name: "李四",
        email: "li4@example.com",
        avatar_url: null,
        has_logged_in: true,
      },
    ]);
    mocks.listIssueAccessRequests.mockResolvedValue({ items: [] });
    mocks.previewIssueAccessControl.mockImplementation((_id, update) =>
      Promise.resolve({
        before: control,
        after: { ...control, ...update },
        subjects_losing_access: [],
        subjects_with_other_source: [],
        affected_effects: ["notifications"],
      }),
    );
    mocks.updateIssueAccessControl.mockResolvedValue(control);
  });

  it("shows project projection, source scope, and the exact restricted semantics", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task permissions" }));

    const dialog = await screen.findByRole("dialog", { name: "Task permissions" });
    expect(within(dialog).getByText("Project · direct")).toBeInTheDocument();
    expect(within(dialog).getByText("project:viewer")).toBeInTheDocument();
    expect(
      within(dialog).getByText(/Direct-parent access still applies in both modes/),
    ).toBeInTheDocument();
  });

  it("supports a projectless task, role bundle and expiry through preview then confirmation", async () => {
    const user = userEvent.setup();
    renderDialog(null);

    await user.click(screen.getByRole("button", { name: "Task permissions" }));

    const dialog = await screen.findByRole("dialog", { name: "Task permissions" });
    expect(within(dialog).getByText(/Projectless task/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Select people" }));
    await user.click(await screen.findByRole("checkbox", { name: "李四" }));
    await user.click(within(dialog).getByRole("combobox", { name: "Task role" }));
    await user.click(await screen.findByRole("option", { name: "Member" }));
    await user.type(within(dialog).getByLabelText("Grant expiry"), "2026-09-20T12:00");
    await user.click(within(dialog).getByRole("button", { name: "Add" }));
    expect(within(dialog).getByText("project.view, project.edit")).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Preview & save" }));

    expect(
      await screen.findByRole("dialog", { name: "Confirm task permission changes" }),
    ).toBeInTheDocument();
    expect(mocks.previewIssueAccessControl).toHaveBeenCalledWith(
      "issue-1",
      expect.objectContaining({
        expected_version: 4,
        grants: [
          expect.objectContaining({
            subject_id: "li-4",
            role: "member",
            scope: "task",
            expires_at: expect.any(String),
          }),
        ],
      }),
    );
    await user.click(screen.getByRole("button", { name: "Confirm update" }));
    await waitFor(() => expect(mocks.updateIssueAccessControl).toHaveBeenCalledTimes(1));
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Task permissions updated");
  });

  it("keeps explanation read-only when the caller has no Manage permission", async () => {
    mocks.getIssueAccessControl.mockRejectedValue(new Error("forbidden"));
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task permissions" }));

    const dialog = await screen.findByRole("dialog", { name: "Task permissions" });
    expect(
      within(dialog).getByText(/only a user with task Manage permission/),
    ).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Preview & save" })).not.toBeInTheDocument();
  });

  it("keeps effective access readable before the writer rollout phase", async () => {
    configStore.getState().setAuthConfig({
      allowSignup: true,
      projectPermissionsEnabled: true,
      projectPermissionRolloutPhase: "reader",
    });
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "Task permissions" }));

    const dialog = await screen.findByRole("dialog", { name: "Task permissions" });
    expect(within(dialog).getByText("Project · direct")).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Preview & save" })).not.toBeInTheDocument();
  });
});
