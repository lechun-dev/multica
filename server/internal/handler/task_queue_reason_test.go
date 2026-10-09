package handler

import "testing"

// 2026-10-09 coder(lq): Only confirmed active-count saturation should be described as full capacity.
func TestTaskQueueReason(t *testing.T) {
	for _, tt := range []struct {
		running int64
		limit   int32
		want    string
	}{
		{0, 1, "awaiting_claim"}, {1, 2, "awaiting_claim"}, {1, 1, "agent_capacity_full"}, {3, 2, "agent_capacity_full"}, {0, 0, "awaiting_claim"},
	} {
		if got := taskQueueReason(tt.running, tt.limit); got != tt.want {
			t.Fatalf("running=%d limit=%d got=%s want=%s", tt.running, tt.limit, got, tt.want)
		}
	}
}

func TestTaskCapacityStatesMatchClaimAdmission(t *testing.T) {
	for _, status := range []string{"dispatched", "running", "waiting_local_directory"} {
		if !taskConsumesAgentCapacity(status) {
			t.Fatalf("active status omitted: %s", status)
		}
	}
	for _, status := range []string{"queued", "deferred", "completed", "failed", "cancelled", ""} {
		if taskConsumesAgentCapacity(status) {
			t.Fatalf("non-active status counted: %s", status)
		}
	}
}
