import type { AgentVisibility } from "../types";

/**
 * Display labels for agent visibility. The DB stores `private` as the value
 * but the UI surface name is "Personal" — it reads better next to "Workspace"
 * and matches the wording used in the access picker.
 */
export const VISIBILITY_LABEL: Record<AgentVisibility, string> = {
  workspace: "Workspace",
  private: "Personal",
};

/** 2026-10-09 coder(lq): Visibility is also invocation authority; administrators keep visibility. */
export const VISIBILITY_DESCRIPTION: Record<AgentVisibility, string> = {
  workspace: "All members can see and run",
  private: "You and workspace administrators can see and run",
};

/** 2026-10-09 coder(lq): Read-only badges describe the same scope used for invocation. */
export const VISIBILITY_TOOLTIP: Record<AgentVisibility, string> = {
  workspace: "Workspace — all members can see and run",
  private: "Personal — visible to the owner, workspace administrators and shared people",
};

export function visibilityLabel(v: AgentVisibility): string {
  return VISIBILITY_LABEL[v];
}
