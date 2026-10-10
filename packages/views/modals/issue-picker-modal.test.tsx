// @vitest-environment jsdom

import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { buildIssueStatusCatalog } from "@multica/core/issue-statuses";
import type { Issue } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";

const searchIssues = vi.fn();

vi.mock("@multica/core/api", () => ({
  api: { searchIssues: (params: unknown) => searchIssues(params) },
}));
// 2026-10-10 coder(lq): The picker now searches through the shared local index.
vi.mock("@multica/core/search-index", () => ({ searchIssues: (params: unknown) => searchIssues(params) }));
vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));
vi.mock("@multica/core/issue-statuses/hooks", () => ({
  useIssueStatuses: () => buildIssueStatusCatalog(undefined),
}));

import { IssuePickerModal } from "./issue-picker-modal";
import { IssueSurfaceVisibilityProvider } from "../issues/surface/visibility-context";

function issue(overrides: Partial<Issue>): Issue {
  return { id: "issue", identifier: "MUL-1", title: "An issue", status: "todo", ...overrides } as Issue;
}

const ORIGINAL = issue({ id: "original", identifier: "MUL-1", title: "Tap targets too small" });
const DUPLICATE = issue({
  id: "duplicate",
  identifier: "MUL-2",
  title: "Tap targets too small on iPhone",
  status: "cancelled",
  duplicate_of: { id: "original", identifier: "MUL-1", title: "Tap targets too small", status: "todo" },
});

afterEach(() => {
  cleanup();
  searchIssues.mockReset();
});

// The mark-as-duplicate picker must not offer an issue that is itself a
// duplicate; the server would reject it after the dialog has already closed.
describe("IssuePickerModal isSelectable", () => {
  it("drops unselectable issues from search results", async () => {
    searchIssues.mockResolvedValue({ issues: [ORIGINAL, DUPLICATE] });
    renderWithI18n(
      <IssueSurfaceVisibilityProvider includeWorkspaceOwned>
      <IssuePickerModal
        open
        onOpenChange={() => {}}
        title="Mark as duplicate"
        description="Pick the original"
        excludeIds={[]}
        isSelectable={(candidate) => !candidate.duplicate_of}
        onSelect={() => {}}
      />
      </IssueSurfaceVisibilityProvider>,
    );

    fireEvent.change(screen.getByPlaceholderText("Search issues..."), { target: { value: "tap" } });
    await waitFor(() => expect(searchIssues).toHaveBeenCalled());
    await screen.findByText("Tap targets too small");
    expect(screen.queryByText("Tap targets too small on iPhone")).toBeNull();
  });

  it("drops unselectable issues from suggestions too", () => {
    renderWithI18n(
      <IssueSurfaceVisibilityProvider includeWorkspaceOwned>
      <IssuePickerModal
        open
        onOpenChange={() => {}}
        title="Mark as duplicate"
        description="Pick the original"
        excludeIds={[]}
        suggestions={[{ key: "recent", heading: "Recently viewed", issues: [DUPLICATE, ORIGINAL] }]}
        isSelectable={(candidate) => !candidate.duplicate_of}
        onSelect={() => {}}
      />
      </IssueSurfaceVisibilityProvider>,
    );

    expect(screen.getByText("Recently viewed")).toBeTruthy();
    expect(screen.getByText("Tap targets too small")).toBeTruthy();
    expect(screen.queryByText("Tap targets too small on iPhone")).toBeNull();
  });
});
