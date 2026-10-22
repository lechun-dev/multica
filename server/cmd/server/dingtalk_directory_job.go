package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/scheduler"
)

const dingtalkDirectoryJobName = "dingtalk_directory_daily"

// 2026-10-20 coder(lq): Reuse durable leases/audit, not process-local cron.
// Only previously linked workspaces participate; never import one enterprise's
// directory into every workspace just because application credentials exist.
func (h *dingtalkLoginHandler) directorySyncJob() scheduler.JobSpec {
	return scheduler.JobSpec{
		Name:              dingtalkDirectoryJobName,
		MaxPlansPerTick:   1,
		RunTimeout:        20 * time.Minute,
		StaleTimeout:      25 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       3,
		RetryBackoff:      []time.Duration{5 * time.Minute, 30 * time.Minute},
		Scopes:            h.directorySyncScopes,
		PlansForScope:     dingtalkDirectoryDailyPlans,
		Handler:           h.runDirectorySyncJob,
	}
}

func (h *dingtalkLoginHandler) directorySyncScopes(ctx context.Context, _ time.Time) ([]scheduler.Scope, error) {
	if h == nil || h.initErr != nil || h.pool == nil || h.host == nil || h.host.ProjectAuth == nil || !h.host.ProjectAuth.Enabled() || h.provider.ClientID == "" || h.provider.ClientSecret == "" {
		return nil, nil
	}
	rows, err := h.pool.Query(ctx, `SELECT w.id::text FROM workspace w WHERE EXISTS
  (SELECT 1 FROM projectauth_organizations o WHERE o.workspace_id=w.id AND o.provider='dingtalk') ORDER BY w.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var scopes []scheduler.Scope
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		scopes = append(scopes, scheduler.Scope{Kind: "workspace", ID: id})
	}
	return scopes, rows.Err()
}

// 2026-10-20 coder(lq): Beijing 03:00 is UTC 19:00 on the previous date.
// Coalesce downtime to the latest due snapshot, but finish retries of the
// current occurrence first. Returning the same slot is safe across restarts.
func dingtalkDirectoryDailyPlans(_ context.Context, _ scheduler.Scope, now time.Time, latest scheduler.LatestPlanInfo) ([]time.Time, error) {
	if latest.Found {
		if latest.Status == "RUNNING" {
			return nil, nil
		}
		if latest.Status == "FAILED" && latest.Attempt < latest.MaxAttempts {
			if latest.RetryEligible(now) {
				return []time.Time{latest.PlanTime}, nil
			}
			return nil, nil
		}
	}
	beijing := now.UTC().Add(8 * time.Hour)
	plan := time.Date(beijing.Year(), beijing.Month(), beijing.Day(), 3, 0, 0, 0, time.UTC).Add(-8 * time.Hour)
	if plan.After(now) {
		plan = plan.Add(-24 * time.Hour)
	}
	if latest.Found && !plan.After(latest.PlanTime) {
		return nil, nil
	}
	return []time.Time{plan}, nil
}

// 2026-10-20 coder(lq): This is an application-authenticated system job, not a
// personal DWS call. Its suspension ceiling is a current enabled workspace
// owner's existing authority; self/owner/cross-workspace safeguards still run
// inside the shared transaction. Never manufacture an HTTP identity/header.
func (h *dingtalkLoginHandler) directorySyncOwner(ctx context.Context, workspaceID string) (string, error) {
	rows, err := h.pool.Query(ctx, `SELECT u.id::text,u.email FROM member m JOIN "user" u ON u.id=m.user_id
  WHERE m.workspace_id=$1 AND m.role='owner' AND u.account_disabled_at IS NULL
   AND EXISTS (SELECT 1 FROM projectauth_organizations o WHERE o.workspace_id=m.workspace_id AND o.provider='dingtalk')
  ORDER BY m.created_at,m.id`, workspaceID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, email string
		if err := rows.Scan(&id, &email); err != nil {
			return "", err
		}
		if !auth.IsTemporarilyDisabledUser(id, email) {
			return id, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", errors.New("DingTalk daily sync requires a linked workspace with an enabled owner")
}

func (h *dingtalkLoginHandler) runDirectorySyncJob(ctx context.Context, in scheduler.HandlerInput) (scheduler.HandlerResult, error) {
	if in.Scope.Kind != "workspace" {
		return scheduler.HandlerResult{}, errors.New("invalid DingTalk directory scope")
	}
	owner, err := h.directorySyncOwner(ctx, in.Scope.ID)
	if err != nil {
		return scheduler.HandlerResult{}, err
	}
	snapshot, departed, result, err := h.loadDingTalkDirectory(ctx, in.Scope.ID, true)
	if err != nil {
		return scheduler.HandlerResult{}, fmt.Errorf("load daily DingTalk directory: %w", err)
	}
	// 2026-10-20 coder(lq): Re-read authority after the potentially slow provider calls.
	currentOwner, err := h.directorySyncOwner(ctx, in.Scope.ID)
	if err != nil {
		return scheduler.HandlerResult{}, err
	}
	if owner != currentOwner {
		return scheduler.HandlerResult{}, errors.New("DingTalk directory sync owner changed during provider read")
	}
	if err := h.persistDingTalkDirectory(ctx, in.Scope.ID, owner, snapshot, departed, &result); err != nil {
		return scheduler.HandlerResult{}, fmt.Errorf("persist daily DingTalk directory: %w", err)
	}
	audit := scheduler.HandlerResult{RowsAffected: int64(result.MembersCreated + result.AccountsDisabled), Result: map[string]any{
		"departure_sync_status":     result.DepartureSyncStatus,
		"organizations_created":     result.OrganizationsCreated,
		"organizations_updated":     result.OrganizationsUpdated,
		"organizations_disabled":    result.OrganizationsDisabled,
		"members_created":           result.MembersCreated,
		"accounts_disabled":         result.AccountsDisabled,
		"accounts_already_disabled": result.AccountsAlreadyDisabled,
		"departures_skipped":        result.DeparturesSkipped,
	}}
	if result.DepartureSyncStatus != "complete" {
		// 2026-10-20 coder(lq): Ordinary directory refresh is useful even when departure permission is
		// missing, but the scheduled occurrence must fail/retry rather than lie.
		return audit, errors.New("DingTalk directory refreshed but departure verification unavailable; no accounts suspended")
	}
	return audit, nil
}
