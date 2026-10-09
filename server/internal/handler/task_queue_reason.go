package handler

import "context"

// 2026-10-09 coder(lq): Queued does not prove saturation. Batch-load the same active states counted by claim admission; failed lookups remain neutral and never break the issue page.
func (h *Handler) hydrateTaskQueueReasons(ctx context.Context, tasks []AgentTaskResponse) {
	workspaces := make(map[string]map[string]string)
	for i := range tasks {
		task := &tasks[i]
		if task.Status != "queued" {
			continue
		}
		reasons, loaded := workspaces[task.WorkspaceID]
		if !loaded {
			reasons = make(map[string]string)
			agents, agentErr := h.Queries.ListAllAgentsAnyKind(ctx, parseUUID(task.WorkspaceID))
			active, taskErr := h.Queries.ListWorkspaceAgentTaskSnapshot(ctx, parseUUID(task.WorkspaceID))
			if agentErr == nil && taskErr == nil {
				counts := make(map[string]int64)
				for _, row := range active {
					if taskConsumesAgentCapacity(row.Status) {
						counts[uuidToString(row.AgentID)]++
					}
				}
				for _, agent := range agents {
					reasons[uuidToString(agent.ID)] = taskQueueReason(counts[uuidToString(agent.ID)], agent.MaxConcurrentTasks)
				}
			}
			workspaces[task.WorkspaceID] = reasons
		}
		task.QueueReason = reasons[task.AgentID]
		if task.QueueReason == "" {
			task.QueueReason = "awaiting_claim"
		}
	}
}
