package handler

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issueposition"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// The checklist is an ordinary issue. Create it in the grant transaction without
// publishing issue:created: the handover flow stages its own recipient-only notification.
func (h *Handler) stageHandoverChecklist(ctx context.Context, tx pgx.Tx, ws, actor string, p *memberHandoverPreview, policy service.IssueCountPolicy) (err error) {
	stage := "load source and workspace"
	defer func() {
		if err != nil {
			err = fmt.Errorf("handover checklist %s: %w", stage, err)
		}
	}()
	var name, slug string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(u.name,''),u.email),COALESCE(w.slug,w.id::text) FROM "user" u CROSS JOIN workspace w WHERE u.id=$1::uuid AND w.id=$2`, p.SourceUserID, ws).Scan(&name, &slug); err != nil {
		return err
	}
	title := "离职工作清单：" + name
	root := "/" + url.PathEscape(slug)
	description := fmt.Sprintf("成员：%s\n\n已开放以下工作的管理权限。原项目负责人和任务负责人保持不变，请决定由自己负责或分配给其他成员。完成此清单不会撤回权限。\n", escapeHandoverMarkdown(name))
	stage = "load resource details"
	rows, err := tx.Query(ctx, `SELECT r.resource_type,r.resource_id::text,
 COALESCE(p.title,i.title,'已删除'),
	 CASE WHEN r.resource_type='project' THEN COALESCE(NULLIF(pu.name,''),pu.email,pa.name,p.lead_type,'未指定')
	 ELSE COALESCE(NULLIF(iu.name,''),iu.email,ia.name,i.assignee_type,'未指定') END
 FROM member_handover_resources r
 LEFT JOIN project p ON r.resource_type='project' AND p.workspace_id=r.workspace_id AND p.id=r.resource_id
 LEFT JOIN issue i ON r.resource_type='issue' AND i.workspace_id=r.workspace_id AND i.id=r.resource_id
 LEFT JOIN "user" pu ON p.lead_type='member' AND pu.id=p.lead_id
 LEFT JOIN "user" iu ON i.assignee_type='member' AND iu.id=i.assignee_id
 LEFT JOIN agent pa ON p.lead_type='agent' AND pa.id=p.lead_id AND pa.workspace_id=r.workspace_id
 LEFT JOIN agent ia ON i.assignee_type='agent' AND ia.id=i.assignee_id AND ia.workspace_id=r.workspace_id
 WHERE r.workspace_id=$1 AND r.source_user_id=$2::uuid AND r.recipient_user_id=$3::uuid
 ORDER BY r.resource_type,r.resource_id`, ws, p.SourceUserID, p.RecipientUserID)
	if err != nil {
		return err
	}
	var details strings.Builder
	for rows.Next() {
		var kind, id, itemTitle, responsible string
		if err = rows.Scan(&kind, &id, &itemTitle, &responsible); err != nil {
			break
		}
		label, path := "任务", "/issues/"
		if kind == "project" {
			label, path = "项目", "/projects/"
		}
		fmt.Fprintf(&details, "\n- %s：[ %s ](%s%s%s)，当前负责人：%s", label, escapeHandoverMarkdown(itemTitle), root, path, url.PathEscape(id), escapeHandoverMarkdown(responsible))
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	description += details.String()
	description = handoverChecklistStart + "\n" + description + "\n" + handoverChecklistEnd
	stage = "find reusable issue"
	var checklistID string
	err = tx.QueryRow(ctx, `SELECT i.id::text FROM member_handovers mh JOIN issue i ON i.workspace_id=mh.workspace_id AND i.id=mh.checklist_issue_id
 WHERE mh.workspace_id=$1 AND mh.source_user_id=$2::uuid AND mh.recipient_user_id=$3::uuid
 AND i.archived_at IS NULL AND i.assignee_type='member' AND i.assignee_id=$3::uuid
 ORDER BY mh.completed_at DESC NULLS LAST,mh.id LIMIT 1 FOR UPDATE OF i`, ws, p.SourceUserID, p.RecipientUserID).Scan(&checklistID)
	q := h.Queries.WithTx(tx)
	if errors.Is(err, pgx.ErrNoRows) {
		stage = "allocate issue number"
		number, e := service.AllocateIssueNumber(ctx, q, parseUUID(ws), policy)
		if e != nil {
			return e
		}
		stage = "allocate issue position"
		position, e := issueposition.NextTopPosition(ctx, tx, parseUUID(ws), "todo")
		if e != nil {
			return e
		}
		stage = "create issue"
		issue, e := q.CreateIssue(ctx, db.CreateIssueParams{ID: dbid.NewV7(), WorkspaceID: parseUUID(ws), Title: title,
			Description: pgtype.Text{String: description, Valid: true}, Status: "todo", Priority: "none", AssigneeType: pgtype.Text{String: "member", Valid: true},
			AssigneeID: parseUUID(p.RecipientUserID), CreatorType: "member", CreatorID: parseUUID(actor), Position: position, Number: number})
		if e != nil {
			return e
		}
		checklistID = uuidToString(issue.ID)
	} else if err != nil {
		return err
	} else {
		// Supplementing work reopens a completed checklist; ordinary completion alone
		// never revokes authorization or causes another notification.
		stage = "read existing issue"
		var previous string
		if err = tx.QueryRow(ctx, `SELECT COALESCE(description,'') FROM issue WHERE workspace_id=$1 AND id=$2::uuid`, ws, checklistID).Scan(&previous); err != nil {
			return err
		}
		description = supplementHandoverChecklist(previous, description)
		stage = "update existing issue"
		_, err = tx.Exec(ctx, `UPDATE issue SET description=$3,status=CASE WHEN issue_effective_status(workspace_id,status) IN ('done','cancelled') THEN 'todo' ELSE status END,
  revision=revision+1,updated_at=now(),last_activity_at=now() WHERE workspace_id=$1 AND id=$2::uuid`, ws, checklistID, description)
		if err != nil {
			return err
		}
	}
	stage = "link issue"
	if _, err = tx.Exec(ctx, `UPDATE member_handovers SET checklist_issue_id=$3::uuid WHERE workspace_id=$1 AND id=$2::uuid`, ws, p.PreviewID, checklistID); err != nil {
		return err
	}
	stage = "read issue status"
	if err = tx.QueryRow(ctx, `SELECT status FROM issue WHERE workspace_id=$1 AND id=$2::uuid`, ws, checklistID).Scan(&p.checklistStatus); err != nil {
		return err
	}
	summary := fmt.Sprintf("📋 **离职工作权限已开放**\n\n成员：%s\n\n本次新增项目：%d；任务：%d。负责人保持不变，请查看清单后分配。", escapeHandoverMarkdown(name), len(p.Projects), len(p.Issues))
	if link := handoverAppLink(slug, "/issues/"+checklistID); link != "" {
		summary += "\n\n[打开完整清单](" + link + ")"
	}
	stage = "create inbox item"
	item, err := q.CreateInboxItem(ctx, db.CreateInboxItemParams{ID: dbid.NewV7(), WorkspaceID: parseUUID(ws), RecipientType: "member", RecipientID: parseUUID(p.RecipientUserID),
		Type: "issue_assigned", Severity: "action_required", IssueID: parseUUID(checklistID), Title: title, Body: pgtype.Text{String: summary, Valid: true},
		ActorType: pgtype.Text{String: "system", Valid: true}, Details: []byte(`{}`)})
	if err != nil {
		return err
	}
	p.inboxItem = &item
	stage = "stage notification"
	return stageHandoverNotification(ctx, tx, ws, p.RecipientUserID, "handover-summary:"+p.PreviewID, checklistID,
		handoverNotificationPayload{HandoverID: p.PreviewID, Text: truncateHandoverText(summary+"\n"+details.String(), 6000)})
}

const handoverChecklistStart = "<!-- multica-handover:start -->"
const handoverChecklistEnd = "<!-- multica-handover:end -->"

// Keep the receiver's notes outside the generated section when work is added.
func supplementHandoverChecklist(previous, generated string) string {
	start := strings.Index(previous, handoverChecklistStart)
	if start >= 0 {
		end := strings.Index(previous[start:], handoverChecklistEnd)
		if end >= 0 {
			end += start + len(handoverChecklistEnd)
			return previous[:start] + generated + previous[end:]
		}
	}
	return strings.TrimSpace(previous) + "\n\n" + generated
}

func truncateHandoverText(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "\n\n更多明细请打开完整清单。"
}

func (h *Handler) publishHandoverInbox(ws string, p *memberHandoverPreview) {
	if h.Bus == nil || p.inboxItem == nil {
		return
	}
	item := p.inboxItem
	h.publish("inbox:new", ws, "system", "", map[string]any{"item": map[string]any{
		"id": uuidToString(item.ID), "workspace_id": ws, "recipient_type": "member", "recipient_id": p.RecipientUserID,
		"type": item.Type, "severity": item.Severity, "issue_id": uuidToString(item.IssueID), "title": item.Title, "body": item.Body.String,
		"read": false, "archived": false, "created_at": item.CreatedAt.Time, "actor_type": "system", "actor_id": nil, "details": map[string]any{}, "issue_status": p.checklistStatus,
	}})
}
