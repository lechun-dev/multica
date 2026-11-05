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

export const VISIBILITY_DESCRIPTION: Record<AgentVisibility, string> = {
  workspace: "All members can see and run",
  private: "Only you can run this agent",
};

/** 2026-11-05 coder(lq): The legacy Personal badge also covers agents shared with specific people. */
export const VISIBILITY_TOOLTIP: Record<AgentVisibility, string> = {
  workspace: "Workspace — all members can see and run",
  private: "Personal — only the owner and people it’s shared with can run it",
};

export function visibilityLabel(v: AgentVisibility): string {
  return VISIBILITY_LABEL[v];
}
