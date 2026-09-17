import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useQuery: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: mocks.useQuery,
}));
vi.mock("@multica/core/api", () => ({
  api: {
    listTaskPermissionRoles: vi.fn(),
    listIssueAccessRequests: vi.fn(),
    createIssueAccessRequest: vi.fn(),
    cancelIssueAccessRequest: vi.fn(),
  },
}));
vi.mock("@multica/core/config", () => ({
  useConfigStore: () => true,
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("../../i18n", async () => {
  const issues = (await import("../../locales/en/issues.json")).default;
  return {
    useT: () => ({ t: (select: (bundle: typeof issues) => string) => select(issues) }),
  };
});

import { RestrictedIssueAccess } from "./restricted-issue-access";

describe("RestrictedIssueAccess", () => {
  it("isolates role and request caches by workspace", () => {
    mocks.useQuery
      .mockReturnValueOnce({ data: { scope: "task", roles: [] } })
      .mockReturnValueOnce({
        data: { items: [{ id: "request-1", requested_role: "viewer", status: "pending" }] },
        refetch: vi.fn(),
      });

    render(<RestrictedIssueAccess issueId="issue-1" identifier="LC-797" />);

    expect(screen.getByText("LC-797")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "You don't have access" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Request access" })).toBeInTheDocument();
    expect(screen.getByText("Can view · Pending approval")).toBeInTheDocument();
    expect(screen.queryByText(/task:viewer|pending/)).not.toBeInTheDocument();
    expect(mocks.useQuery).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({
        queryKey: ["task-permission-roles", "workspace-1", "restricted"],
      }),
    );
    expect(mocks.useQuery).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({
        queryKey: ["issue-access-requests", "workspace-1", "issue-1", "mine"],
      }),
    );
  });
});
