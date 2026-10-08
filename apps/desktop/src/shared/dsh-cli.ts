export type DshCliState = "ready" | "not_installed" | "error";

export interface DshCliStatus {
  state: DshCliState;
  reason?: "probe_failed" | "probe_timeout" | "protocol_incompatible" | "launch_failed";
}
