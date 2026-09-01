"use client";

import { createContext, useContext, type ReactNode } from "react";
import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useAuthStore } from "@multica/core/auth";
import { useIssueViewStore } from "@multica/core/issues/stores/view-store";
import { memberListOptions } from "@multica/core/workspace/queries";

/**
 * Task-surface visibility scope shared by row decorations and the canonical
 * issue queries. Keeping this in the surface layer avoids coupling the core
 * Agent query helpers to a particular page preference.
 *
 * 2026-09-01 coder(lq): Keep the default inclusive for embedded surfaces that
 * do not opt into the task-list visibility scope yet.
 */
interface IssueSurfaceVisibility {
  includeWorkspaceOwned: boolean;
  ready: boolean;
}

const IssueSurfaceVisibilityContext =
  createContext<IssueSurfaceVisibility | null>(null);

export function IssueSurfaceVisibilityProvider({
  includeWorkspaceOwned,
  ready = true,
  children,
}: {
  includeWorkspaceOwned: boolean;
  /** Whether the current user's workspace membership has been resolved. */
  ready?: boolean;
  children: ReactNode;
}) {
  return (
    <IssueSurfaceVisibilityContext.Provider
      value={{ includeWorkspaceOwned, ready }}
    >
      {children}
    </IssueSurfaceVisibilityContext.Provider>
  );
}

export function useIssueSurfaceIncludeWorkspaceOwned(): boolean {
  return useContext(IssueSurfaceVisibilityContext)?.includeWorkspaceOwned ?? true;
}

export function useIssueSurfaceVisibilityReady(): boolean {
  return useContext(IssueSurfaceVisibilityContext)?.ready ?? true;
}

/**
 * Workspace-wide visibility state for surfaces that are not descendants of an
 * IssueSurface provider (Inbox and Chat). Those surfaces still need to honor
 * the same owner toggle, so they derive it from the singleton issue view
 * store and workspace membership when no local provider is present.
 */
export function useWorkspaceTaskVisibility(): IssueSurfaceVisibility {
  const context = useContext(IssueSurfaceVisibilityContext);
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const currentUser = useAuthStore((state) => state.user);
  const showWorkspaceOwnedItems = useIssueViewStore(
    (state) => state.showWorkspaceOwnedItems,
  );
  // 2026-09-01 coder(lq): Desktop chrome mounts before a workspace route is
  // resolved; keep this shared hook fail-closed without calling a workspace
  // endpoint with an empty id.
  const membersQuery = useQuery({
    ...memberListOptions(wsId),
    enabled: !!wsId,
  });
  const members = membersQuery.data ?? [];
  const isWorkspaceOwner = useMemo(
    () =>
      !!currentUser &&
      members.some(
        (member) => member.user_id === currentUser.id && member.role === "owner",
      ),
    [currentUser, members],
  );
  if (context) return context;
  const ready = !!wsId && membersQuery.isSuccess;
  return {
    ready,
    includeWorkspaceOwned: ready
      ? !isWorkspaceOwner || showWorkspaceOwnedItems
      : false,
  };
}
