import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const pinKeys = {
  all: (wsId: string, userId: string) => ["pins", wsId, userId] as const,
  list: (wsId: string, userId: string) => [...pinKeys.all(wsId, userId), "list"] as const,
};

export function pinListOptions(
  wsId: string,
  userId: string,
  includeWorkspaceOwned = true,
) {
  return queryOptions({
    queryKey: includeWorkspaceOwned
      ? pinKeys.list(wsId, userId)
      : ([...pinKeys.list(wsId, userId), { includeWorkspaceOwned }] as const),
    queryFn: () => api.listPins(includeWorkspaceOwned),
  });
}
