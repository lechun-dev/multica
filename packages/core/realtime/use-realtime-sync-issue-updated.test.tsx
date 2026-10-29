// @vitest-environment jsdom
import { QueryClient, QueryClientProvider, QueryObserver } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { WSClient } from "../api/ws-client";
import { issueKeys } from "../issues/queries";
import { inboxKeys } from "../inbox/queries";
import { projectKeys } from "../projects/queries";
import type { Issue } from "../types";
import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

vi.mock("../platform/workspace-storage", () => ({
  getCurrentWsId: () => "ws-1",
  getCurrentSlug: () => "test-ws",
  createWorkspaceAwareStorage: (adapter: unknown) => adapter,
  registerForWorkspaceRehydration: () => {},
}));
vi.mock("../paths", () => ({
  useHasOnboarded: () => true,
  resolvePostAuthDestination: () => "/",
}));

describe("useRealtimeSync — permission-safe issue update hints", () => {
  let qc: QueryClient;
  let receive: (payload: unknown) => void;
  let unmount: () => void;

  beforeEach(() => {
    vi.useFakeTimers();
    qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    const ws = {
      on: (event: string, handler: (payload: unknown) => void) => {
        if (event === "issue:updated") receive = handler;
        return () => {};
      },
      onAny: () => () => {},
      onReconnect: () => () => {},
    } as unknown as WSClient;
    const stores = {
      authStore: Object.assign(() => ({}), {
        getState: () => ({ user: { id: "u1" } }),
      }),
    } as unknown as RealtimeSyncStores;
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    );
    ({ unmount } = renderHook(() => useRealtimeSync(ws, stores), { wrapper }));
  });

  afterEach(() => {
    unmount();
    qc.clear();
    vi.useRealTimers();
  });

  it("refetches a redacted status update without claiming the hint is a full snapshot", async () => {
    const key = issueKeys.detail("ws-1", "issue-1");
    const old = { id: "issue-1", workspace_id: "ws-1", title: "任务", status: "todo", revision: 1 } as Issue;
    qc.setQueryData(key, old);
    const fetch = vi.fn(async () => ({ ...old, status: "done", revision: 2 }));
    const observer = new QueryObserver(qc, { queryKey: key, queryFn: fetch });
    const unsubscribe = observer.subscribe(() => {});

    // Exact business-field omission used by safeIssueProjection on the server.
    receive({ issue: { id: old.id, workspace_id: "ws-1", project_id: null, parent_issue_id: null, revision: 2 }, status_changed: true });

    expect(qc.getQueryData(key)).toEqual(old);
    await vi.advanceTimersByTimeAsync(100);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(qc.getQueryData(key)).toMatchObject({ status: "done", revision: 2 });
    unsubscribe();
  });

  it("refreshes all task view shapes and detail variants, including off-window membership", async () => {
    const old = { id: "issue-1", status: "todo", revision: 1 };
    const detail = issueKeys.detail("ws-1", old.id);
    const affected = [
      detail,
      [...detail, { includeWorkspaceOwned: false }],
      issueKeys.identifier("ws-1", "LC-2477"),
      issueKeys.listSorted("ws-1"),
      issueKeys.myListSorted("ws-1", "assigned", {}),
      issueKeys.flat("ws-1", "workspace", {}),
      issueKeys.tableAll("ws-1"),
      issueKeys.assigneeGroupsAll("ws-1"),
      issueKeys.myAssigneeGroupsAll("ws-1"),
      issueKeys.projectGantt("ws-1", "project-1"),
      issueKeys.children("ws-1", "parent-1"),
      issueKeys.childrenByParents("ws-1", ["parent-1"]),
      issueKeys.childProgress("ws-1"),
      inboxKeys.list("ws-1"),
      inboxKeys.archived("ws-1"),
      inboxKeys.facets("ws-1"),
      projectKeys.list("ws-1"),
    ];
    const untouched = [
      issueKeys.detail("ws-1", "other"),
      issueKeys.identifier("ws-1", "LC-OTHER"),
      issueKeys.detail("ws-2", old.id),
      issueKeys.listSorted("ws-2"),
      inboxKeys.list("ws-2"),
      issueKeys.timeline(old.id),
      issueKeys.attachments(old.id),
    ];
    for (const key of affected) qc.setQueryData(key, old);
    for (const key of untouched) qc.setQueryData(key, { ...old, id: "other" });
    // View data may have no copy of the changed issue: it can ENTER a filter.
    qc.setQueryData(issueKeys.listSorted("ws-1"), { byStatus: {} });
    qc.setQueryData(issueKeys.flat("ws-1", "workspace", {}), { pages: [{ issues: [] }], pageParams: [0] });

    receive({ issue: { id: old.id, revision: 2 }, status_changed: true });
    await vi.advanceTimersByTimeAsync(100);

    expect(qc.getQueryData(detail)).toEqual(old);
    for (const key of affected) expect(qc.getQueryState(key)?.isInvalidated, JSON.stringify(key)).toBe(true);
    for (const key of untouched) expect(qc.getQueryState(key)?.isInvalidated, JSON.stringify(key)).toBe(false);
  });

  it("coalesces rapid, duplicate and out-of-order hints and reads the final status", async () => {
    const key = issueKeys.detail("ws-1", "issue-1");
    const old = { id: "issue-1", status: "done", revision: 1 };
    qc.setQueryData(key, old);
    const fetch = vi.fn(async () => ({ ...old, status: "todo", revision: 4 }));
    const observer = new QueryObserver(qc, { queryKey: key, queryFn: fetch });
    const unsubscribe = observer.subscribe(() => {});
    const listKey = issueKeys.listSorted("ws-1");
    qc.setQueryData(listKey, { byStatus: {} });
    const fetchList = vi.fn(async () => ({ byStatus: {} }));
    const listObserver = new QueryObserver(qc, { queryKey: listKey, queryFn: fetchList });
    const unsubscribeList = listObserver.subscribe(() => {});

    for (const revision of [2, 4, 3, 4]) {
      receive({ issue: { id: old.id, revision }, status_changed: true });
      await vi.advanceTimersByTimeAsync(20);
    }
    // Events for another issue share the same list refresh.
    receive({ issue_id: "issue-2" });
    expect(fetch).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(20);

    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetchList).toHaveBeenCalledTimes(1);
    expect(qc.getQueryData(key)).toMatchObject({ status: "todo", revision: 4 });
    unsubscribe();
    unsubscribeList();
  });

  it("processes a new batch while the previous batch's HTTP read is still pending", async () => {
    const key = issueKeys.detail("ws-1", "issue-1");
    const old = { id: "issue-1", status: "todo", revision: 1 };
    qc.setQueryData(key, old);
    let resolveFirst!: (value: typeof old) => void;
    const firstRead = new Promise<typeof old>((resolve) => { resolveFirst = resolve; });
    const fetch = vi.fn()
      .mockImplementationOnce(() => firstRead)
      .mockResolvedValue({ ...old, status: "done", revision: 3 });
    const observer = new QueryObserver(qc, { queryKey: key, queryFn: fetch });
    const unsubscribe = observer.subscribe(() => {});

    receive({ issue: { id: old.id, revision: 2 } });
    await vi.advanceTimersByTimeAsync(100);
    expect(fetch).toHaveBeenCalledTimes(1);
    receive({ issue: { id: old.id, revision: 3 } });
    await vi.advanceTimersByTimeAsync(100);

    expect(fetch).toHaveBeenCalledTimes(2);
    expect(qc.getQueryData(key)).toMatchObject({ status: "done", revision: 3 });
    resolveFirst({ ...old, status: "in_progress", revision: 2 });
    await vi.advanceTimersByTimeAsync(0);
    expect(qc.getQueryData(key)).toMatchObject({ status: "done", revision: 3 });
    unsubscribe();
  });

  it.each([false, true])("prevents a pre-event HTTP response from winning (initial read: %s)", async (initialRead) => {
    const key = [...issueKeys.detail("ws-1", "issue-1"), { includeWorkspaceOwned: false }];
    const old = { id: "issue-1", status: "todo", revision: 1 };
    if (!initialRead) qc.setQueryData(key, old);
    let resolveOld!: (value: typeof old) => void;
    const staleRequest = new Promise<typeof old>((resolve) => { resolveOld = resolve; });
    const fetch = vi.fn()
      .mockImplementationOnce(() => staleRequest)
      .mockResolvedValue({ ...old, status: "done", revision: 2 });
    const observer = new QueryObserver(qc, { queryKey: key, queryFn: fetch });
    const unsubscribe = observer.subscribe(() => {});
    if (!initialRead) void observer.refetch();
    expect(fetch).toHaveBeenCalledTimes(1);

    receive({ issue: { id: old.id, revision: 2 }, status_changed: true });
    await vi.advanceTimersByTimeAsync(100);
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(qc.getQueryData(key)).toMatchObject({ status: "done", revision: 2 });
    resolveOld(old);
    await vi.advanceTimersByTimeAsync(0);
    expect(qc.getQueryData(key)).toMatchObject({ status: "done", revision: 2 });
    unsubscribe();
  });

  it("retains complete-snapshot support without rolling back on older or duplicate events", () => {
    const key = issueKeys.detail("ws-1", "issue-1");
    const old = { id: "issue-1", title: "任务", status: "todo", revision: 1 };
    qc.setQueryData(key, old);
    receive({ issue: { ...old, status: "done", revision: 3 }, status_changed: true });
    receive({ issue: { ...old, revision: 2 } });
    receive({ issue: { ...old, revision: 3 } });
    expect(qc.getQueryData(key)).toMatchObject({ status: "done", revision: 3 });
  });

  it("clears queued refreshes when the realtime subscription unmounts", async () => {
    const key = issueKeys.detail("ws-1", "issue-1");
    qc.setQueryData(key, { id: "issue-1", revision: 1 });
    receive({ issue: { id: "issue-1", revision: 2 } });
    unmount();
    await vi.advanceTimersByTimeAsync(100);
    expect(qc.getQueryState(key)?.isInvalidated).toBe(false);
  });
});
