package service

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 2026-09-03 coder(lq): Keep configurable retry matching isolated from the
// task lifecycle service so policy changes do not spread through claim paths.
var retryHTTPStatusPattern = regexp.MustCompile(`\b([1-5][0-9]{2})\b`)

type retryPlan struct {
	Allowed       bool
	MaxAttempts   int32
	Delay         time.Duration
	PolicyMatched bool
}

// retryPlanForTask preserves the built-in retry rules and augments them with
// the first enabled workspace policy that matches the failure. A malformed or
// unavailable policy row is ignored; a policy store outage must never turn a
// failed task into a transaction failure or prevent the built-in retry path.
func (s *TaskService) retryPlanForTask(ctx context.Context, task db.AgentTaskQueue, reason, errorText string) retryPlan {
	plan := retryPlan{
		Allowed:     retryableReasons[reason],
		MaxAttempts: retryAttemptCeiling(reason, task.MaxAttempts),
		Delay:       retryDelayForAttempt(reason, task.Attempt),
	}
	if task.AutopilotRunID.Valid || !retrySourceAvailable(task) {
		plan.Allowed = false
		return plan
	}

	agent, err := s.Queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		return finalizeRetryPlan(plan, task)
	}
	policies, err := s.Queries.ListTaskRetryPolicies(ctx, agent.WorkspaceID)
	if err != nil {
		return finalizeRetryPlan(plan, task)
	}
	for _, policy := range policies {
		if !policy.Enabled || !matchesRetryPolicy(policy, reason, errorText) {
			continue
		}
		plan.PolicyMatched = true
		plan.Allowed = task.Attempt < policy.MaxAttempts
		plan.MaxAttempts = policy.MaxAttempts
		plan.Delay = retryPolicyDelay(policy.DelaySchedule, task.Attempt)
		// Policy priority is already applied by ListTaskRetryPolicies. The
		// first enabled match owns the decision, including a budget denial.
		break
	}
	return finalizeRetryPlan(plan, task)
}

func finalizeRetryPlan(plan retryPlan, task db.AgentTaskQueue) retryPlan {
	if task.AutopilotRunID.Valid || !retrySourceAvailable(task) || task.Attempt >= plan.MaxAttempts {
		plan.Allowed = false
	}
	return plan
}

func retrySourceAvailable(task db.AgentTaskQueue) bool {
	return task.IssueID.Valid || task.ChatSessionID.Valid || isSourceContextQuickCreateTask(task)
}

func matchesRetryPolicy(policy db.TaskRetryPolicy, reason, errorText string) bool {
	switch policy.MatchType {
	case "failure_reason":
		return reason == policy.MatchValue
	case "http_status":
		for _, match := range retryHTTPStatusPattern.FindAllStringSubmatch(errorText, -1) {
			if len(match) == 2 && match[1] == strings.TrimSpace(policy.MatchValue) {
				return true
			}
		}
		return false
	case "error_contains":
		return strings.Contains(strings.ToLower(errorText), strings.ToLower(policy.MatchValue))
	default:
		return false
	}
}

func retryPolicyDelay(raw []byte, failedAttempt int32) time.Duration {
	var schedule []int
	if json.Unmarshal(raw, &schedule) != nil || len(schedule) == 0 {
		return 0
	}
	index := int(failedAttempt - 1)
	if index < 0 {
		index = 0
	}
	if index >= len(schedule) {
		index = len(schedule) - 1
	}
	return time.Duration(schedule[index]) * time.Second
}

// TaskRetryPolicySweepResult is intentionally small so the sweeper can emit
// useful counters without exposing database rows or policy internals.
type TaskRetryPolicySweepResult struct {
	Scanned int
	Retried int
	Skipped int
}

// RetryFailedTasksByPolicy scans a bounded batch of terminal tasks and routes
// each through MaybeRetryFailedTask. The existing agent_task_queue row and
// CreateRetryTask uniqueness checks provide durable deduplication; no separate
// failure queue is needed.
func (s *TaskService) RetryFailedTasksByPolicy(ctx context.Context, limit int32) (TaskRetryPolicySweepResult, error) {
	if limit <= 0 {
		return TaskRetryPolicySweepResult{}, nil
	}
	ids, err := s.Queries.ListFailedTaskIDsForRetry(ctx, db.ListFailedTaskIDsForRetryParams{Limit: limit})
	if err != nil {
		return TaskRetryPolicySweepResult{}, err
	}
	result := TaskRetryPolicySweepResult{Scanned: len(ids)}
	for _, id := range ids {
		task, err := s.Queries.GetAgentTask(ctx, id)
		if err != nil {
			result.Skipped++
			continue
		}
		child, err := s.MaybeRetryFailedTask(ctx, task)
		if err != nil {
			result.Skipped++
			continue
		}
		if child != nil {
			result.Retried++
		} else {
			result.Skipped++
		}
	}
	return result, nil
}
