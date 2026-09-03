import { queryOptions } from "@tanstack/react-query";
import { api, ApiError } from "../api";

export const projectKeys = {
  all: (wsId: string) => ["projects", wsId] as const,
  list: (wsId: string) => [...projectKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "detail", id] as const,
  detailAllowMissing: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "detail", id, "allow-missing"] as const,
};

export function projectListOptions(wsId: string, includeWorkspaceOwned = true) {
  return queryOptions({
    queryKey: includeWorkspaceOwned
      ? projectKeys.list(wsId)
      : [...projectKeys.list(wsId), false] as const,
    queryFn: () =>
      api.listProjects(
        includeWorkspaceOwned ? undefined : { include_workspace_owned: false },
      ),
    select: (data) => data.projects,
  });
}

export function projectDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: projectKeys.detail(wsId, id),
    queryFn: () => api.getProject(id),
  });
}

// 2026-09-03 coder(lq): Issue details can remain accessible after a project
// is archived or hidden. Keep this fallback on a separate query key so normal
// project pages still surface a genuine 404 instead of silently rendering an
// empty project.
export function projectDetailAllowMissingOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: projectKeys.detailAllowMissing(wsId, id),
    queryFn: async () => {
      try {
        return await api.getProject(id);
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) {
          return null;
        }
        throw error;
      }
    },
    retry: false,
  });
}
