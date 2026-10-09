"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { workspaceRuntimeModelListOptions } from "../workspace-runtime-models/queries";
import { mergeRuntimeModelCatalog } from "./model-catalog";
import { runtimeModelsOptions } from "./models";

export function useRuntimeModelCatalog(
  workspaceId: string,
  runtimeId: string | null,
  runtimeOnline: boolean,
) {
  // 2026-10-09 coder(lq): Default picker opens must stay usable from configured/cache catalogs; live daemon discovery is best-effort background refresh.
  const discovery = useQuery(runtimeModelsOptions(runtimeOnline ? runtimeId : null));
  // 2026-10-09 coder(lq): Fetch configuration independently so slow/failed
  // discovery and offline runtimes never hide the workspace's configured list.
  const configured = useQuery({
    ...workspaceRuntimeModelListOptions(workspaceId),
    enabled: Boolean(workspaceId && runtimeId),
  });
  const models = useMemo(
    () => mergeRuntimeModelCatalog(discovery.data?.models, configured.data),
    [discovery.data?.models, configured.data],
  );
  return {
    isLoading: discovery.isLoading,
    isError: discovery.isError,
    error: discovery.error,
    models,
    supported:
      discovery.data?.supported !== false ||
      configured.data?.some((model) => model.enabled) === true,
    isCatalogLoading: discovery.isLoading || configured.isLoading,
    configurationError: configured.error,
  };
}
