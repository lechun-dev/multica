import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { IssueDetailRoute } from "./issue-detail-route";

const state = vi.hoisted(() => ({ searchParams: new URLSearchParams(), replace: vi.fn() }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/issues/canonical-id", () => ({
  useCanonicalIssue: () => ({ canonicalId: "issue-1", issue: { identifier: "LC-71" }, isResolving: false, notFound: false }),
}));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/lechun/issues/${id}` }),
}));
vi.mock("../../navigation", () => ({ useNavigation: () => state }));
vi.mock("../surface/visibility-context", () => ({
  useWorkspaceTaskVisibility: () => ({ includeWorkspaceOwned: true, ready: true }),
}));
vi.mock("./restricted-issue-access", () => ({ RestrictedIssueAccessFallback: () => null }));
vi.mock("./issue-detail", () => ({
  IssueDetail: ({ issueId, accessRequestId }: { issueId: string; accessRequestId?: string }) => <div data-testid="detail" data-issue={issueId} data-request={accessRequestId} />,
  IssueDetailSkeleton: () => null,
  IssueNotFound: () => null,
}));

describe("DingTalk access request landing", () => {
  beforeEach(() => { state.replace.mockClear(); state.searchParams = new URLSearchParams(); });

  it("opens the named request and preserves it when canonicalizing a task UUID", () => {
    state.searchParams.set("access_request", "request-2");
    render(<IssueDetailRoute routeId="issue-1" />);
    expect(screen.getByTestId("detail")).toHaveAttribute("data-issue", "issue-1");
    expect(screen.getByTestId("detail")).toHaveAttribute("data-request", "request-2");
    expect(state.replace).toHaveBeenCalledWith("/lechun/issues/LC-71?access_request=request-2");
  });

  it("does not arm approval on ordinary task links", () => {
    render(<IssueDetailRoute routeId="LC-71" />);
    expect(screen.getByTestId("detail")).not.toHaveAttribute("data-request");
    expect(state.replace).not.toHaveBeenCalled();
  });
});
