export type DshCliState =
  | "ready"
  | "needs_repair"
  | "not_installed"
  | "unsupported"
  | "error";

export interface DshCliStatus {
  state: DshCliState;
}
