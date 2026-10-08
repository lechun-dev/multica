export type DshCliState =
  | "ready"
  | "needs_repair"
  | "not_installed"
  | "unsupported"
  | "error";

export interface DshCliStatus {
  state: DshCliState;
  reason?: "probe_failed" | "command_conflict" | "registration_failed" | "permission_denied";
}
