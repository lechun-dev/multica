package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/projectauth"
)

// Evidence is written in the directory transaction, after the account access
// safety checks. A disabled account alone never qualifies for automation.
func (h *Handler) RecordDingTalkHandoverEvidence(ctx context.Context, tx pgx.Tx, workspaceID string, verified, current []string) error {
	if _, err := tx.Exec(ctx, `UPDATE member_handover_automation SET eligible=false WHERE workspace_id=$1 AND source_user_id=ANY($2::uuid[])`, workspaceID, current); err != nil {
		return err
	}
	for _, source := range verified {
		if _, err := tx.Exec(ctx, `INSERT INTO member_handover_automation(workspace_id,source_user_id) VALUES($1,$2::uuid)
   ON CONFLICT(workspace_id,source_user_id) DO UPDATE SET eligible=true,verified_at=now()`, workspaceID, source); err != nil {
			return err
		}
	}
	return nil
}

// The separate latch fails closed if even the reporting-line failure marker
// could not be saved. Current staff do not gain departure eligibility here.
func (h *Handler) RecordDingTalkManagerSyncOutcomes(ctx context.Context, tx pgx.Tx, workspaceID string, current, failed []string) error {
	if _, err := tx.Exec(ctx, `UPDATE member_handover_automation SET manager_sync_failed=false
 WHERE workspace_id=$1 AND source_user_id=ANY($2::uuid[])`, workspaceID, current); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO member_handover_automation(workspace_id,source_user_id,eligible,manager_sync_failed)
 SELECT $1,id,false,true FROM unnest($2::uuid[]) AS id
 ON CONFLICT(workspace_id,source_user_id) DO UPDATE SET manager_sync_failed=true`, workspaceID, failed)
	return err
}

type MemberHandoverRunResult struct{ Completed, NoWork, Pending, Failed int }

// RunAutomaticMemberHandovers processes durable departure evidence independently
// of directory synchronization. Each member has a bounded transaction.
func (h *Handler) RunAutomaticMemberHandovers(ctx context.Context, workspaceID, actorID string) (MemberHandoverRunResult, error) {
	result := MemberHandoverRunResult{}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT a.source_user_id::text,m.id::text FROM member_handover_automation a
  JOIN member m ON m.workspace_id=a.workspace_id AND m.user_id=a.source_user_id
  WHERE a.workspace_id=$1 AND a.eligible ORDER BY a.source_user_id`, workspaceID)
	if err != nil {
		tx.Rollback(ctx)
		return result, err
	}
	type candidate struct{ source, member string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.source, &c.member); err != nil {
			break
		}
		candidates = append(candidates, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	tx.Rollback(ctx)
	if err != nil {
		return result, err
	}
	var persistenceErrors []error
	for _, c := range candidates {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		status, reason, err := h.runAutomaticMemberHandover(attemptCtx, workspaceID, actorID, c.member)
		cancel()
		if err != nil {
			slog.ErrorContext(ctx, "automatic member handover failed", "workspace_id", workspaceID, "source_user_id", c.source, "error", err)
			status, reason = "failed", "execution_failed"
			result.Failed++
		} else if status == "pending" {
			result.Pending++
		} else if status == "completed" {
			result.Completed++
		} else if status == "no_work" {
			result.NoWork++
		}
		if status == "pending" || status == "failed" {
			if saveErr := h.recordAutomaticHandoverProblem(ctx, workspaceID, c.source, c.member, actorID, status, reason); saveErr != nil {
				slog.ErrorContext(ctx, "automatic member handover status persistence failed", "workspace_id", workspaceID, "source_user_id", c.source, "error", saveErr)
				persistenceErrors = append(persistenceErrors, fmt.Errorf("save handover status for %s: %w", c.source, saveErr))
				if status == "pending" {
					result.Pending--
					result.Failed++
				}
			}
		}
	}
	return result, errors.Join(persistenceErrors...)
}

func lockHandoverPeople(ctx context.Context, tx pgx.Tx, ids []string) error {
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`SELECT id FROM "user" WHERE id=ANY($1::uuid[]) ORDER BY id FOR SHARE`, []any{ids}},
		{`SELECT id FROM member WHERE user_id=ANY($1::uuid[]) ORDER BY id FOR SHARE`, []any{ids}},
	} {
		rows, err := tx.Query(ctx, statement.query, statement.args...)
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) runAutomaticMemberHandover(ctx context.Context, ws, actor, memberID string) (string, string, error) {
	policy := service.ResolveIssueCountPolicy(ctx, h.Entitlements, parseUUID(ws))
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`); err != nil {
		return "", "", err
	}
	if err = configureHandoverTransaction(ctx, tx); err != nil {
		return "", "", err
	}
	// Serializable reads revalidate directory evidence without holding its global lock.
	for _, key := range []string{"member_handover:" + ws + ":" + memberID} {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
			return "", "", err
		}
	}
	source, err := validateHandoverPeople(ctx, tx, ws, memberID, actor, "")
	if err != nil {
		return automaticHandoverValidation(err)
	}
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT eligible FROM member_handover_automation WHERE workspace_id=$1 AND source_user_id=$2::uuid`, ws, source).Scan(&eligible); err != nil {
		return "", "", err
	}
	if !eligible {
		return "skipped", "", nil
	}
	projects, issues, err := loadHandoverResources(ctx, tx, ws, source, false)
	if err != nil {
		return "", "", err
	}
	if len(projects)+len(issues) == 0 {
		if _, err = tx.Exec(ctx, `UPDATE member_handover_automation SET status=CASE WHEN handover_id IS NULL THEN 'no_work' ELSE 'completed' END,reason='',attempted_at=now() WHERE workspace_id=$1 AND source_user_id=$2::uuid`, ws, source); err != nil {
			return "", "", err
		}
		return "no_work", "", tx.Commit(ctx)
	}
	p := memberHandoverPreview{SourceUserID: source, ManagerStatus: "not_synced", ManagerSyncStatus: "not_synced"}
	if err = loadHandoverManager(ctx, tx, ws, &p); err != nil {
		return "", "", err
	}
	if p.ManagerSyncStatus == "failed" {
		return "pending", "manager_read_failed", nil
	}
	if p.ManagerStatus != "ready" || p.ManagerUserID == source {
		return "pending", "manager_" + p.ManagerStatus, nil
	}
	p.RecipientUserID = p.ManagerUserID
	if _, err = validateHandoverPeople(ctx, tx, ws, memberID, actor, p.RecipientUserID); err != nil {
		return automaticHandoverValidation(err)
	}
	// Serializable reads protect the suspension ceiling; account/membership
	// locks are acquired only at commit so resource processing cannot hold them.
	allowed, err := h.Queries.WithTx(tx).ListManageableAccounts(ctx, db.ListManageableAccountsParams{WorkspaceID: parseUUID(ws), ActorID: parseUUID(actor)})
	if err != nil {
		return "", "", err
	}
	manageable := false
	for _, id := range allowed {
		if uuidToString(id) == source {
			manageable = true
		}
	}
	if !manageable {
		return "pending", "source_not_manageable", nil
	}
	p.Projects, p.Issues, err = loadHandoverResources(ctx, tx, ws, source, true)
	if err != nil {
		return "", "", err
	}
	if len(p.Projects)+len(p.Issues) == 0 {
		_, err = tx.Exec(ctx, `UPDATE member_handover_automation SET status=CASE WHEN handover_id IS NULL THEN 'no_work' ELSE 'completed' END,reason='',attempted_at=now() WHERE workspace_id=$1 AND source_user_id=$2::uuid`, ws, source)
		if err == nil {
			err = tx.Commit(ctx)
		}
		return "no_work", "", err
	}
	body, _ := json.Marshal(p)
	err = tx.QueryRow(ctx, `INSERT INTO member_handovers(workspace_id,source_member_id,source_user_id,recipient_user_id,actor_id,preview,expires_at,automatic)
  VALUES($1,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::jsonb,now()+interval '15 minutes',true) RETURNING id::text`, ws, memberID, source, p.RecipientUserID, actor, body).Scan(&p.PreviewID)
	if err != nil {
		return "", "", err
	}
	if err = h.applyMemberHandover(ctx, tx, ws, actor, &p, policy); err != nil {
		return "", "", err
	}
	if err = lockHandoverPeople(ctx, tx, []string{actor, source, p.RecipientUserID}); err != nil {
		return "", "", err
	}
	if _, err = validateHandoverPeople(ctx, tx, ws, memberID, actor, p.RecipientUserID); err != nil {
		return automaticHandoverValidation(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", err
	}
	h.publishHandoverInbox(ws, &p)
	if h.Bus != nil {
		h.publish("member:handover", ws, "system", "", map[string]any{"handover_id": p.PreviewID})
	}
	return "completed", "", nil
}

func automaticHandoverValidation(err error) (string, string, error) {
	var invalid *handoverHTTPError
	if errors.As(err, &invalid) {
		return "pending", "people_unavailable", nil
	}
	return "", "", err
}

func (h *Handler) recordAutomaticHandoverProblem(ctx context.Context, ws, source, memberID, owner, status, reason string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = configureHandoverTransaction(ctx, tx); err != nil {
		return err
	}
	// Recheck outstanding work under the same locks as execution. A completed
	// manual or concurrent run must not be overwritten by a stale failed attempt.
	for _, key := range []string{"member_handover:" + ws + ":" + memberID} {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
			return err
		}
	}
	projects, issues, err := loadHandoverResources(ctx, tx, ws, source, false)
	if err != nil {
		return err
	}
	if len(projects)+len(issues) == 0 {
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE member_handover_automation SET status=$3,reason=$4,attempted_at=now()
 WHERE workspace_id=$1 AND source_user_id=$2::uuid AND eligible`, ws, source, status, reason)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (h *Handler) applyMemberHandover(ctx context.Context, tx pgx.Tx, ws, actor string, p *memberHandoverPreview, policy service.IssueCountPolicy) error {
	repo := &projectAuthRepository{db: tx}
	for _, group := range []struct {
		kind  string
		items []handoverResource
	}{{"project", p.Projects}, {"issue", p.Issues}} {
		for _, item := range group.items {
			var err error
			if group.kind == "project" {
				err = repo.PromoteProjectMember(ctx, item.ID, p.RecipientUserID, projectauth.ProjectRole(item.Role))
				if err == nil {
					err = recordHandoverGrant(ctx, tx, ws, item.ID, "", p.RecipientUserID, item.Role, actor, p.PreviewID)
				}
			} else {
				err = recordHandoverGrant(ctx, tx, ws, item.ProjectID, item.ID, p.RecipientUserID, item.Role, actor, p.PreviewID)
			}
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO member_handover_resources(workspace_id,source_user_id,resource_type,resource_id,recipient_user_id,handover_id)
    VALUES($1,$2::uuid,$3,$4::uuid,$5::uuid,$6::uuid)`, ws, p.SourceUserID, group.kind, item.ID, p.RecipientUserID, p.PreviewID); err != nil {
				return err
			}
		}
	}
	if err := h.stageHandoverChecklist(ctx, tx, ws, actor, p, policy); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE member_handovers SET completed_at=now() WHERE id=$1::uuid AND workspace_id=$2`, p.PreviewID, ws); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE member_handover_automation SET status='completed',reason='',recipient_user_id=$3::uuid,handover_id=$4::uuid,attempted_at=now() WHERE workspace_id=$1 AND source_user_id=$2::uuid`, ws, p.SourceUserID, p.RecipientUserID, p.PreviewID)
	return err
}

func configureHandoverTransaction(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='1s'`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SET LOCAL statement_timeout='10s'`)
	return err
}

func handoverProblemText(reason string) string {
	switch reason {
	case "manager_read_failed":
		return "直属主管读取失败，需确认关系后重试"
	case "manager_not_synced":
		return "尚未保存直属主管"
	case "manager_unset":
		return "未设置直属主管"
	case "manager_unlinked":
		return "直属主管未关联工作区账号"
	case "manager_inactive":
		return "直属主管已停用"
	case "source_not_manageable":
		return "当前 owner 无权管理此账号的全部工作区"
	case "execution_failed":
		return "权限开放失败，后台将独立重试"
	default:
		return "账号或接收人不可用"
	}
}
