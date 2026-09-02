import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const projectKeys = {
  all: (wsId: string) => ["projects", wsId] as const,
  list: (wsId: string) => [...projectKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "detail", id] as const,
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
