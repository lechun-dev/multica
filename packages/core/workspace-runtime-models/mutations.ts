import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import type {
  UpdateWorkspaceRuntimeModelRequest,
  WorkspaceRuntimeModelRequest,
} from "../types";
import { workspaceRuntimeModelKeys } from "./queries";
import { runtimeModelsKeys } from "../runtimes/models";

// 2026-10-09 coder(lq): Runtime responses also include configured entries;
// refresh both catalogs after edits. Do not make saving wait for CLI discovery.
function invalidateModelCatalogs(client: QueryClient, workspaceId: string) {
  void client.invalidateQueries({ queryKey: runtimeModelsKeys.all() });
  return client.invalidateQueries({
    queryKey: workspaceRuntimeModelKeys.list(workspaceId),
  });
}

export function useCreateWorkspaceRuntimeModel() {
  const queryClient = useQueryClient();
  const workspaceId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: WorkspaceRuntimeModelRequest) =>
      api.createWorkspaceRuntimeModel(workspaceId, data),
    onSettled: () => invalidateModelCatalogs(queryClient, workspaceId),
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
    onSettled: () => invalidateModelCatalogs(queryClient, workspaceId),
  });
}

export function useDeleteWorkspaceRuntimeModel() {
  const queryClient = useQueryClient();
  const workspaceId = useWorkspaceId();
  return useMutation({
    mutationFn: (modelId: string) =>
      api.deleteWorkspaceRuntimeModel(workspaceId, modelId),
    onSettled: () => invalidateModelCatalogs(queryClient, workspaceId),
  });
}
