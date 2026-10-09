package handler

// 2026-10-09 coder(lq): Keep admission-hint classification pure so its boundaries can be tested without a database.
func taskQueueReason(running int64, limit int32) string {
	if limit > 0 && running >= int64(limit) {
		return "agent_capacity_full"
	}
	return "awaiting_claim"
}

func taskConsumesAgentCapacity(status string) bool {
	switch status {
	case "dispatched", "running", "waiting_local_directory":
		return true
	default:
		return false
	}
}
