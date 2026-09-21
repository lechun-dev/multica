import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import type {
  UpdateWorkspaceRuntimeModelRequest,
  WorkspaceRuntimeModelRequest,
} from "../types";
import { workspaceRuntimeModelKeys } from "./queries";

export function useCreateWorkspaceRuntimeModel() {
  const queryClient = useQueryClient();
  const workspaceId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: WorkspaceRuntimeModelRequest) =>
      api.createWorkspaceRuntimeModel(workspaceId, data),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: workspaceRuntimeModelKeys.list(workspaceId),
      }),
  });
}

export function useUpdateWorkspaceRuntimeModel() {
  const queryClient = useQueryClient();
  const workspaceId = useWorkspaceId();
  return useMutation({
    mutationFn: ({
      modelId,
      ...data
    }: { modelId: string } & UpdateWorkspaceRuntimeModelRequest) =>
      api.updateWorkspaceRuntimeModel(workspaceId, modelId, data),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: workspaceRuntimeModelKeys.list(workspaceId),
      }),
  });
}

export function useDeleteWorkspaceRuntimeModel() {
  const queryClient = useQueryClient();
  const workspaceId = useWorkspaceId();
  return useMutation({
    mutationFn: (modelId: string) =>
      api.deleteWorkspaceRuntimeModel(workspaceId, modelId),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: workspaceRuntimeModelKeys.list(workspaceId),
      }),
  });
}
