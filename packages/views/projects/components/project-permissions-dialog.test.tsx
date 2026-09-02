import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const {
  listProjectMembers,
  listMembers,
  addProjectMember,
  removeProjectMember,
  toastSuccess,
  toastError,
} = vi.hoisted(() => ({
  listProjectMembers: vi.fn(),
  listMembers: vi.fn(),
  addProjectMember: vi.fn(),
  removeProjectMember: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    listProjectMembers,
    listMembers,
    addProjectMember,
    removeProjectMember,
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

import { ProjectPermissionsDialog } from "./project-permissions-dialog";

const workspaceMembers = [
  {
    id: "membership-alice",
    workspace_id: "workspace-1",
    user_id: "alice",
    role: "member",
    created_at: "2026-08-27T00:00:00Z",
    name: "Alice Owner",
    email: "alice@example.com",
    avatar_url: null,
  },
  {
    id: "membership-bob",
    workspace_id: "workspace-1",
    user_id: "bob",
    role: "member",
    created_at: "2026-08-27T00:00:00Z",
    name: "Bob Builder",
    email: "bob@example.com",
    avatar_url: null,
  },
  {
    id: "membership-carol",
    workspace_id: "workspace-1",
    user_id: "carol",
    role: "member",
    created_at: "2026-08-27T00:00:00Z",
    name: "Carol Reviewer",
    email: "carol@example.com",
    avatar_url: null,
  },
  {
    id: "membership-dave",
    workspace_id: "workspace-1",
    user_id: "dave",
    role: "member",
    created_at: "2026-08-27T00:00:00Z",
    name: "Dave Designer",
    email: "dave@example.com",
    avatar_url: null,
  },
];

function renderDialog() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <ProjectPermissionsDialog projectId="project-1" />
    </QueryClientProvider>,
  );
}

describe("ProjectPermissionsDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listProjectMembers.mockResolvedValue({
      members: [
        { project_id: "project-1", user_id: "alice", role: "owner" },
        { project_id: "project-1", user_id: "bob", role: "member" },
      ],
      total: 2,
      can_manage: true,
    });
    listMembers.mockResolvedValue(workspaceMembers);
    addProjectMember.mockResolvedValue(undefined);
    removeProjectMember.mockResolvedValue(undefined);
  });

  it("only shows the authorization action to project member managers", async () => {
    listProjectMembers.mockResolvedValue({ members: [], total: 0, can_manage: false });

    renderDialog();

    await waitFor(() => expect(listProjectMembers).toHaveBeenCalledWith("project-1"));
    expect(screen.queryByRole("button", { name: "Access" })).not.toBeInTheDocument();
  });

  it("shows all current grants separately from ungranted workspace members", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.click(await screen.findByRole("button", { name: "Access" }));
    expect(await screen.findByRole("dialog", { name: "Project access" })).toBeInTheDocument();

    const currentAccess = await screen.findByRole("region", { name: "Current project access" });
    expect(within(currentAccess).getByText("Alice Owner")).toBeInTheDocument();
    expect(within(currentAccess).getByText("Bob Builder")).toBeInTheDocument();
    expect(within(currentAccess).queryByText("Carol Reviewer")).not.toBeInTheDocument();

    const addMembers = screen.getByRole("region", { name: "Add members" });
    expect(within(addMembers).queryByText("Alice Owner")).not.toBeInTheDocument();
    expect(within(addMembers).queryByText("Bob Builder")).not.toBeInTheDocument();
    expect(within(addMembers).getByText("Carol Reviewer")).toBeInTheDocument();

    await user.type(screen.getByRole("textbox", { name: "Search by name or email" }), "carol@example");

    expect(within(addMembers).getByText("Carol Reviewer")).toBeInTheDocument();
    expect(within(addMembers).queryByText("Dave Designer")).not.toBeInTheDocument();
  });

  it("changes one existing project member role directly", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.click(await screen.findByRole("button", { name: "Access" }));
    await user.click(await screen.findByRole("combobox", { name: "Change role for Bob Builder" }));
    await user.click(await screen.findByRole("option", { name: "Manager" }));

    await waitFor(() => {
      expect(addProjectMember).toHaveBeenCalledWith("project-1", { user_id: "bob", role: "manager" });
    });
    expect(toastSuccess).toHaveBeenCalledWith("Project access updated");
  });

  it("grants one selected role to multiple workspace members", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.click(await screen.findByRole("button", { name: "Access" }));
    await screen.findByText("Carol Reviewer");
    await user.click(screen.getByRole("checkbox", { name: "Carol Reviewer" }));
    await user.click(screen.getByRole("checkbox", { name: "Dave Designer" }));
    expect(screen.getByRole("checkbox", { name: "Carol Reviewer" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Dave Designer" })).toBeChecked();

    const rolePicker = screen.getByRole("combobox", { name: "Role for selected members" });
    await user.click(rolePicker);
    await user.click(await screen.findByRole("option", { name: "Viewer" }));
    await user.click(screen.getByRole("button", { name: "Grant access" }));

    await waitFor(() => expect(addProjectMember).toHaveBeenCalledTimes(2));
    expect(addProjectMember).toHaveBeenCalledWith("project-1", { user_id: "carol", role: "viewer" });
    expect(addProjectMember).toHaveBeenCalledWith("project-1", { user_id: "dave", role: "viewer" });
    expect(toastSuccess).toHaveBeenCalledWith("Project access updated");
  });

  it("keeps only failed members selected after a partial bulk failure", async () => {
    addProjectMember
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error("grant failed"));
    const user = userEvent.setup();
    renderDialog();

    await user.click(await screen.findByRole("button", { name: "Access" }));
    await screen.findByText("Carol Reviewer");
    await user.click(screen.getByRole("checkbox", { name: "Carol Reviewer" }));
    await user.click(screen.getByRole("checkbox", { name: "Dave Designer" }));
    expect(screen.getByRole("checkbox", { name: "Carol Reviewer" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Dave Designer" })).toBeChecked();
    await user.click(screen.getByRole("button", { name: "Grant access" }));

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("Some members could not be updated. Try again."));
    expect(screen.getByText(/Selected\s+1/)).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Carol Reviewer" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Dave Designer" })).toBeChecked();
  });

  it("removes an existing project member", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.click(await screen.findByRole("button", { name: "Access" }));
    await user.click(await screen.findByRole("button", { name: "Remove project access for Alice Owner" }));

    await waitFor(() => expect(removeProjectMember).toHaveBeenCalledWith("project-1", "alice"));
    expect(toastSuccess).toHaveBeenCalledWith("Project member removed");
  });
});
