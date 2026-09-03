import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const { listProjectAuthorizationOrganizations } = vi.hoisted(() => ({
  listProjectAuthorizationOrganizations: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: { listProjectAuthorizationOrganizations },
}));

import { ProjectAuthorizationDirectory } from "./project-authorization-directory";

const organizations = [
  {
    id: "org-engineering",
    workspace_id: "workspace-1",
    provider: "manual",
    external_id: "engineering",
    name: "Engineering",
    status: "active",
  },
  {
    id: "org-platform",
    workspace_id: "workspace-1",
    provider: "manual",
    external_id: "platform",
    name: "Platform",
    parent_id: "org-engineering",
    status: "active",
  },
  {
    id: "org-sales",
    workspace_id: "workspace-1",
    provider: "manual",
    external_id: "sales",
    name: "Sales",
    status: "active",
  },
];

const members = [
  {
    organization_id: "org-engineering",
    user_id: "alice",
    name: "Alice Zhang",
    email: "alice@example.com",
    workspace_role: "member",
  },
  {
    organization_id: "org-platform",
    user_id: "alice",
    name: "Alice Zhang",
    email: "alice@example.com",
    workspace_role: "member",
  },
  {
    organization_id: "org-platform",
    user_id: "bob",
    name: "Bob Chen",
    email: "bob@example.com",
    workspace_role: "admin",
  },
  {
    organization_id: "org-sales",
    user_id: "carol",
    name: "Carol Li",
    email: "carol@example.com",
    workspace_role: "member",
  },
];

function renderDirectory() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <ProjectAuthorizationDirectory workspaceId="workspace-1" />
    </QueryClientProvider>,
  );
}

describe("ProjectAuthorizationDirectory", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listProjectAuthorizationOrganizations.mockResolvedValue({
      organizations,
      members,
      total: organizations.length,
      member_total: members.length,
    });
  });

  it("renders the department tree and all unique people by default", async () => {
    renderDirectory();

    expect(await screen.findByText("Engineering")).toBeInTheDocument();
    expect(screen.getByText("Platform")).toBeInTheDocument();
    expect(screen.getByText("Sales")).toBeInTheDocument();
    expect(screen.getByText("Alice Zhang")).toBeInTheDocument();
    expect(screen.getByText("Bob Chen")).toBeInTheDocument();
    expect(screen.getByText("Carol Li")).toBeInTheDocument();
    expect(screen.getAllByText("Alice Zhang")).toHaveLength(1);
  });

  it("includes descendants when a parent department is selected", async () => {
    const user = userEvent.setup();
    renderDirectory();

    await user.click((await screen.findByText("Engineering")).closest("button")!);

    expect(screen.getByText("Alice Zhang")).toBeInTheDocument();
    expect(screen.getByText("Bob Chen")).toBeInTheDocument();
    expect(screen.queryByText("Carol Li")).not.toBeInTheDocument();
  });

  it("filters people by name or email", async () => {
    const user = userEvent.setup();
    renderDirectory();

    const search = await screen.findByRole("textbox", { name: "Search name or email" });
    await user.type(search, "carol@example");

    expect(screen.getByText("Carol Li")).toBeInTheDocument();
    expect(screen.queryByText("Alice Zhang")).not.toBeInTheDocument();
    expect(screen.queryByText("Bob Chen")).not.toBeInTheDocument();
  });

  it("shows people even when no departments have been imported", async () => {
    listProjectAuthorizationOrganizations.mockResolvedValueOnce({
      organizations: [],
      members: [
        {
          organization_id: "",
          user_id: "david",
          name: "David Wu",
          email: "david@example.com",
          workspace_role: "member",
        },
      ],
      total: 0,
      member_total: 1,
    });

    renderDirectory();

    expect((await screen.findAllByText("All people")).length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("David Wu")).toBeInTheDocument();
  });
});
