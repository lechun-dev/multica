import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { MemberWithUser } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { ProjectMemberMultiSelect } from "./project-member-multi-select";

const members: MemberWithUser[] = [
  {
    id: "membership-alice",
    workspace_id: "workspace-1",
    user_id: "alice",
    role: "member",
    created_at: "2026-09-06T00:00:00Z",
    name: "Alice Zhang",
    email: "alice@example.com",
    avatar_url: null,
    has_logged_in: true,
  },
  {
    id: "membership-bob",
    workspace_id: "workspace-1",
    user_id: "bob",
    role: "member",
    created_at: "2026-09-06T00:00:00Z",
    name: "Bob Chen",
    email: "bob@example.com",
    avatar_url: null,
    has_logged_in: false,
  },
];

describe("ProjectMemberMultiSelect", () => {
  it("marks members who have not logged in as not registered", async () => {
    const user = userEvent.setup();
    renderWithI18n(
      <ProjectMemberMultiSelect
        members={members}
        selectedIds={new Set()}
        onToggle={() => undefined}
        onSelectAll={() => undefined}
        onClear={() => undefined}
        placeholder="Select people"
        selectedLabel="Selected"
        selectAllLabel="Select all"
        clearLabel="Clear"
        noResultsLabel="No results"
        loadingLabel="Loading"
        errorLabel="Error"
        removeLabel="Remove"
      />,
    );

    await user.click(screen.getByRole("button", { name: "Select people" }));

    expect(screen.getByText("Alice Zhang")).toBeInTheDocument();
    expect(screen.getByText("Bob Chen (Not registered)")).toBeInTheDocument();
  });
});
