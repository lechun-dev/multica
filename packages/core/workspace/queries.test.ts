// @vitest-environment node
import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import type { Agent, Workspace } from "../types";
import {
  agentListOptions,
  invalidateMemberHandover,
  workspaceBySlugOptions,
  workspaceKeys,
} from "./queries";

function makeWorkspace(slug: string): Workspace {
  return {
    id: `id-${slug}`,
    name: slug,
    slug,
    description: null,
    context: null,
    settings: {},
    repos: [],
    issue_prefix: slug.toUpperCase(),
    avatar_url: null,
    created_at: "",
    updated_at: "",
  };
}

describe("workspaceBySlugOptions", () => {
  const workspaces = [makeWorkspace("acme")];

  it("selects a matching workspace", () => {
    expect(workspaceBySlugOptions("acme").select?.(workspaces)).toEqual(
      workspaces[0],
    );
  });

  it("returns null after an authoritative list omits the slug", () => {
    expect(workspaceBySlugOptions("missing").select?.(workspaces)).toBeNull();
  });
});

describe("agentListOptions", () => {
  it("polls only while projected runtime availability can age offline", () => {
    const options = agentListOptions("ws-1");
    const interval = options.refetchInterval;
    expect(typeof interval).toBe("function");
    if (typeof interval !== "function") return;

    const queryState = (data: Agent[]) =>
      interval({ state: { status: "success", data } } as never);

    expect(
      queryState([{ runtime_availability: "online" } as Agent]),
    ).toBe(30_000);
    expect(
      queryState([{ runtime_availability: "unstable" } as Agent]),
    ).toBe(30_000);
    expect(
      queryState([{ runtime_availability: "offline" } as Agent]),
    ).toBe(false);
    expect(
      queryState([{ runtime_availability: undefined } as Agent]),
    ).toBe(false);
    expect(
      queryState([
        {
          archived_at: "2026-09-04T00:00:00Z",
          runtime_availability: "online",
        } as Agent,
      ]),
    ).toBe(false);
    expect(queryState([])).toBe(false);
  });
});

describe("invalidateMemberHandover", () => {
  it("refreshes work and access projections only in the affected workspace", async () => {
    const client = new QueryClient();
    const affected = [
      workspaceKeys.members("ws-1"),
      workspaceKeys.assigneeFrequency("ws-1"),
      ["issues", "ws-1", "detail", "issue-1"],
      ["projects", "ws-1", "list"],
      ["dashboard", "ws-1"],
      ["project-members", "ws-1", "project-1"],
      ["project-access-grants", "ws-1", "project-1"],
      ["project-permissions", "ws-1", "project-1"],
      ["issue-access-control", "ws-1", "issue-1"],
      ["issue-effective-access", "ws-1", "issue-1"],
      ["issue-access-requests", "ws-1", "issue-1"],
      ["effective-access-report", "ws-1"],
    ];
    const unaffected = [
      workspaceKeys.members("ws-2"),
      workspaceKeys.assigneeFrequency("ws-2"),
      ["issues", "ws-2", "detail", "issue-1"],
      ["projects", "ws-2", "list"],
      ["dashboard", "ws-2"],
      ["project-permissions", "ws-2", "project-1"],
      workspaceKeys.agents("ws-1"),
    ];
    for (const key of [...affected, ...unaffected]) {
      client.setQueryData(key, {});
    }

    await invalidateMemberHandover(client, "ws-1");

    for (const key of affected) {
      expect(client.getQueryState(key)?.isInvalidated, JSON.stringify(key)).toBe(true);
    }
    for (const key of unaffected) {
      expect(client.getQueryState(key)?.isInvalidated, JSON.stringify(key)).toBe(false);
    }
    client.clear();
  });
});
