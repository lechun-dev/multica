import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, renderHook, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { ApiError, setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { NavigationProvider } from "../../navigation";
import type { NavigationAdapter } from "../../navigation";
import {
  IssueDetailRoute,
  parseCommentHighlightHash,
  useCanonicalIssueUrl,
} from "./issue-detail-route";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("../../i18n", async () => {
  const issues = (await import("../../locales/en/issues.json")).default;
  return {
    useT: () => ({ t: (select: (bundle: typeof issues) => string) => select(issues) }),
  };
});

vi.mock("../surface/visibility-context", () => ({
  useWorkspaceTaskVisibility: () => ({ includeWorkspaceOwned: true, ready: true }),
}));

vi.mock("./restricted-issue-access", () => ({
  RestrictedIssueAccess: ({ identifier }: { identifier: string }) => (
    <div>
      <p>没有访问权限：{identifier}</p>
      <button type="button">申请权限</button>
    </div>
  ),
}));

vi.mock("@multica/core/paths", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/paths")>(
    "@multica/core/paths",
  );
  return {
    ...actual,
    useCurrentWorkspace: () => ({ id: "ws-1", name: "Acme", slug: "acme" }),
    useWorkspacePaths: () => actual.paths.workspace("acme"),
  };
});

const replace = vi.fn();
const push = vi.fn();

function wrapper({ children }: { children: ReactNode }) {
  const adapter: NavigationAdapter = {
    push,
    replace,
    back: vi.fn(),
    pathname: "/acme/issues/x",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (p: string) => `https://app.multica.com${p}`,
  };
  return <NavigationProvider value={adapter}>{children}</NavigationProvider>;
}

describe("useCanonicalIssueUrl", () => {
  beforeEach(() => {
    replace.mockClear();
    push.mockClear();
  });

  it("rewrites a UUID URL to the identifier once the issue resolves", () => {
    const { rerender } = renderHook(
      ({ identifier }: { identifier?: string }) =>
        useCanonicalIssueUrl("cb240efb-154c-42a8-ae92-42b02676feca", identifier),
      { wrapper, initialProps: {} },
    );

    // Nothing to rewrite to while the issue is still loading.
    expect(replace).not.toHaveBeenCalled();

    rerender({ identifier: "TRS-134" });
    expect(replace).toHaveBeenCalledWith("/acme/issues/TRS-134");
    expect(push).not.toHaveBeenCalled();
  });

  it("leaves an already-canonical URL alone", () => {
    renderHook(() => useCanonicalIssueUrl("TRS-134", "TRS-134"), { wrapper });
    expect(replace).not.toHaveBeenCalled();
  });

  // `useWorkspacePaths()` returns a fresh object per call, so the effect's
  // dependencies change identity on every commit. Without the ref guard this
  // re-fired the replace forever.
  it("rewrites once, not on every render", () => {
    const { rerender } = renderHook(
      () => useCanonicalIssueUrl("cb240efb-154c-42a8-ae92-42b02676feca", "TRS-134"),
      { wrapper },
    );

    rerender();
    rerender();
    expect(replace).toHaveBeenCalledTimes(1);
  });

  // A lowercase key resolves server-side, so the URL must still be normalized
  // to the issue's real identifier rather than left as typed.
  it("normalizes a differently-cased identifier segment", () => {
    renderHook(() => useCanonicalIssueUrl("trs-134", "TRS-134"), { wrapper });
    expect(replace).toHaveBeenCalledWith("/acme/issues/TRS-134");
  });

  it("preserves a source-comment deep link while canonicalizing a UUID", () => {
    renderHook(
      () => useCanonicalIssueUrl(
        "cb240efb-154c-42a8-ae92-42b02676feca",
        "TRS-134",
        "#comment-comment-7",
      ),
      { wrapper },
    );
    expect(replace).toHaveBeenCalledWith("/acme/issues/TRS-134#comment-comment-7");
  });
});

describe("parseCommentHighlightHash", () => {
  it.each([
    ["#comment-01a02814-f098-7309-8286-0b249c66884d", "01a02814-f098-7309-8286-0b249c66884d"],
    ["#comment-comment_7", "comment_7"],
    ["#activity", undefined],
    ["#comment-", undefined],
    ["#comment-unsafe/value", undefined],
  ])("maps %s to %s", (hash, expected) => {
    expect(parseCommentHighlightHash(hash)).toBe(expected);
  });
});

describe("IssueDetailRoute with an identifier that names no issue", () => {
  // Regression: the route used to fall through to IssueDetail with the raw
  // identifier. IssueDetail mounted a second observer on the query that had
  // just failed, `retryOnMount` refetched it, the route flipped back to its
  // skeleton and unmounted IssueDetail, then remounted it when the refetch
  // failed — an unbounded request loop that never reached "not found".
  // Retry is off so any count above 1 can only be a remount refetch.
  it("settles on not-found without looping requests", async () => {
    replace.mockClear();
    push.mockClear();
    const getIssue = vi.fn().mockRejectedValue(new Error("issue not found"));
    const getIssueAccessRequestTarget = vi
      .fn()
      .mockRejectedValue(new ApiError("task not found", 404, "Not Found"));
    setApiInstance({ getIssue, getIssueAccessRequestTarget } as unknown as ApiClient);
    const qc = new QueryClient({
      defaultOptions: { queries: { staleTime: Infinity, retry: false } },
    });

    const { rerender } = render(
      <QueryClientProvider client={qc}>
        <NavigationProvider
          value={{
            push,
            replace,
            back: vi.fn(),
            pathname: "/acme/issues/ZZZ-134",
            searchParams: new URLSearchParams(),
            hash: "",
            getShareableUrl: (p: string) => `https://app.multica.com${p}`,
          }}
        >
          <IssueDetailRoute routeId="ZZZ-134" />
        </NavigationProvider>
      </QueryClientProvider>,
    );

    await waitFor(() => expect(getIssue).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByText("Task deleted")).toBeInTheDocument());
    await new Promise((resolve) => setTimeout(resolve, 250));
    expect(getIssue).toHaveBeenCalledTimes(1);
    expect(getIssueAccessRequestTarget).toHaveBeenCalledTimes(1);

    rerender(
      <QueryClientProvider client={qc}>
        <NavigationProvider
          value={{
            push,
            replace,
            back: vi.fn(),
            pathname: "/acme/issues/ZZZ-134",
            searchParams: new URLSearchParams(),
            hash: "",
            getShareableUrl: (p: string) => `https://app.multica.com${p}`,
          }}
        >
          <IssueDetailRoute routeId="ZZZ-134" />
        </NavigationProvider>
      </QueryClientProvider>,
    );
    await new Promise((resolve) => setTimeout(resolve, 250));
    expect(getIssue).toHaveBeenCalledTimes(1);

    // A failed resolve must never rewrite the URL.
    expect(replace).not.toHaveBeenCalled();
    qc.clear();
  });

  it("shows an access request action when the task still exists", async () => {
    const getIssue = vi.fn().mockRejectedValue(new Error("forbidden"));
    const getIssueAccessRequestTarget = vi.fn().mockResolvedValue({
      id: "issue-1",
      identifier: "LC-797",
    });
    setApiInstance({ getIssue, getIssueAccessRequestTarget } as unknown as ApiClient);
    const qc = new QueryClient({
      defaultOptions: { queries: { staleTime: Infinity, retry: false } },
    });

    render(
      <QueryClientProvider client={qc}>
        <NavigationProvider
          value={{
            push,
            replace,
            back: vi.fn(),
            pathname: "/acme/issues/LC-797",
            searchParams: new URLSearchParams(),
            hash: "",
            getShareableUrl: (p: string) => `https://app.multica.com${p}`,
          }}
        >
          <IssueDetailRoute routeId="LC-797" />
        </NavigationProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByText("没有访问权限：LC-797")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "申请权限" })).toBeInTheDocument();
    expect(screen.queryByText("Task deleted")).not.toBeInTheDocument();
    qc.clear();
  });

  it("does not report a temporary access check failure as deletion", async () => {
    const getIssue = vi.fn().mockRejectedValue(new Error("forbidden"));
    const getIssueAccessRequestTarget = vi
      .fn()
      .mockRejectedValue(new ApiError("unavailable", 503, "Service Unavailable"));
    setApiInstance({ getIssue, getIssueAccessRequestTarget } as unknown as ApiClient);
    const qc = new QueryClient({
      defaultOptions: { queries: { staleTime: Infinity, retry: false } },
    });

    render(
      <QueryClientProvider client={qc}>
        <NavigationProvider
          value={{
            push,
            replace,
            back: vi.fn(),
            pathname: "/acme/issues/LC-797",
            searchParams: new URLSearchParams(),
            hash: "",
            getShareableUrl: (p: string) => `https://app.multica.com${p}`,
          }}
        >
          <IssueDetailRoute routeId="LC-797" />
        </NavigationProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByText("Could not check task access.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    expect(screen.queryByText("Task deleted")).not.toBeInTheDocument();
    qc.clear();
  });
});
