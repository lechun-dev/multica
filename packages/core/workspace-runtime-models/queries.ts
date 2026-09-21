import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const workspaceRuntimeModelKeys = {
  all: (workspaceId: string) => ["workspace-runtime-models", workspaceId] as const,
  list: (workspaceId: string) => [
    ...workspaceRuntimeModelKeys.all(workspaceId),
    "list",
  ] as const,
};

export function workspaceRuntimeModelListOptions(workspaceId: string) {
  return queryOptions({
    queryKey: workspaceRuntimeModelKeys.list(workspaceId),
    queryFn: () => api.listWorkspaceRuntimeModels(workspaceId),
    enabled: workspaceId.length > 0,
  });
}
