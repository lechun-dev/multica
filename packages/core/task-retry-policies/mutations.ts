import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { taskRetryPolicyKeys } from "./queries";
import type { TaskRetryPolicyRequest, UpdateTaskRetryPolicyRequest } from "../types";

export function useCreateTaskRetryPolicy() {
  const queryClient = useQueryClient();
  const workspaceId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: TaskRetryPolicyRequest) => api.createTaskRetryPolicy(workspaceId, data),
    onSettled: () => queryClient.invalidateQueries({ queryKey: taskRetryPolicyKeys.list(workspaceId) }),
  });
}

export function useUpdateTaskRetryPolicy() {
  const queryClient = useQueryClient();
  const workspaceId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ policyId, ...data }: { policyId: string } & UpdateTaskRetryPolicyRequest) =>
      api.updateTaskRetryPolicy(workspaceId, policyId, data),
    onSettled: () => queryClient.invalidateQueries({ queryKey: taskRetryPolicyKeys.list(workspaceId) }),
  });
}

export function useDeleteTaskRetryPolicy() {
  const queryClient = useQueryClient();
  const workspaceId = useWorkspaceId();
  return useMutation({
    mutationFn: (policyId: string) => api.deleteTaskRetryPolicy(workspaceId, policyId),
    onSettled: () => queryClient.invalidateQueries({ queryKey: taskRetryPolicyKeys.list(workspaceId) }),
  });
}
