import { queryOptions } from "@tanstack/react-query";
import { api } from "./api";

// 2026-10-10 coder(lq): Keep mobile comment deletion aligned with the server config after upstream synchronization.
export function appConfigOptions() {
  return queryOptions({ queryKey: ["app-config"], queryFn: () => api.getAppConfig(), staleTime: 60_000 });
}
