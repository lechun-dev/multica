import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const taskRetryPolicyKeys = {
  all: (workspaceId: string) => ["task-retry-policies", workspaceId] as const,
  list: (workspaceId: string) => [...taskRetryPolicyKeys.all(workspaceId), "list"] as const,
};

export function taskRetryPolicyListOptions(workspaceId: string) {
  return queryOptions({
    queryKey: taskRetryPolicyKeys.list(workspaceId),
    queryFn: () => api.listTaskRetryPolicies(workspaceId),
    enabled: workspaceId.length > 0,
  });
}
