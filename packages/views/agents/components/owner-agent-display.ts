import {
  deriveAgentActivity,
  deriveAgentAvailability,
  deriveWorkload,
  type AgentPresenceDetail,
} from "@multica/core/agents";
import type { OwnerAgentCatalogEntry } from "@multica/core/api/schemas";
import type { Agent, AgentRuntime } from "@multica/core/types";

export function agentListModel(agent: Agent | OwnerAgentCatalogEntry): string {
  return ("access_scope" in agent ? agent.display?.model : agent.model) ?? "";
}

export function deriveOwnerAgentDisplay(
  agent: OwnerAgentCatalogEntry,
  runtime: AgentRuntime | null,
  now: number,
) {
  if (!agent.display) return null;
  const { activity, max_concurrent_tasks: capacity } = agent.display;
  const archived = !!agent.archived_at;
  const counts = {
    runningCount: archived ? 0 : agent.display.running_count,
    queuedCount: archived ? 0 : agent.display.queued_count,
  };
  const presence: AgentPresenceDetail = {
    availability: archived ? "archived" : deriveAgentAvailability(runtime, now),
    workload: deriveWorkload(counts),
    ...counts,
    capacity,
  };
  return { presence, activity: deriveAgentActivity(activity, agent.created_at, now) };
}
