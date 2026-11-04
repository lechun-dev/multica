package handler

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type memberHandoverStatus struct {
	Status             string  `json:"status"`
	Reason             string  `json:"reason"`
	RecipientName      string  `json:"recipient_name"`
	AttemptedAt        *string `json:"attempted_at"`
	NotificationStatus string  `json:"notification_status"`
}

// Keep employee handover diagnostics owner-only. The ordinary member response
// remains compatible with clients that do not understand the extra field.
func loadMemberHandoverStatuses(ctx context.Context, tx pgx.Tx, ws, actor string) (map[string]*memberHandoverStatus, error) {
	result := map[string]*memberHandoverStatus{}
	// Completed manual handovers are evidence of work transfer, not evidence of
	// departure. Read their audit records without granting automatic eligibility.
	rows, err := tx.Query(ctx, `WITH completed AS (
 SELECT DISTINCT ON (source_user_id) source_user_id,recipient_user_id,id AS handover_id,completed_at AS attempted_at
 FROM member_handovers WHERE workspace_id=$1 AND completed_at IS NOT NULL
 ORDER BY source_user_id,completed_at DESC,id DESC
), outcomes AS (
 SELECT source_user_id,recipient_user_id,handover_id,attempted_at,'completed'::text AS status,''::text AS reason,1 AS priority FROM completed
 UNION ALL
 SELECT source_user_id,recipient_user_id,handover_id,attempted_at,status,reason,0 AS priority
 FROM member_handover_automation a WHERE workspace_id=$1 AND eligible
 AND NOT (status='no_work' AND EXISTS(SELECT 1 FROM completed c WHERE c.source_user_id=a.source_user_id))
), latest AS (
 SELECT DISTINCT ON (source_user_id) * FROM outcomes
 ORDER BY source_user_id,attempted_at DESC NULLS LAST,priority DESC
)
 SELECT a.source_user_id::text,a.status,a.reason,COALESCE(NULLIF(u.name,''),u.email,''),a.attempted_at::text,
COALESCE(notifications.status,'')
 FROM latest a LEFT JOIN "user" u ON u.id=a.recipient_user_id
 JOIN member source_member ON source_member.workspace_id=$1 AND source_member.user_id=a.source_user_id
 JOIN "user" source_user ON source_user.id=a.source_user_id AND source_user.account_disabled_at IS NOT NULL
 LEFT JOIN LATERAL (
  SELECT CASE WHEN bool_or(o.status='failed') THEN 'failed'
   WHEN bool_or(n.status='pending') THEN 'pending'
   WHEN bool_and(o.status='delivered') THEN 'delivered' ELSE 'staged' END AS status
  FROM member_handover_notifications n LEFT JOIN dingtalk_notify_outbox o
   ON o.workspace_id=n.workspace_id::text AND o.event_id=n.event_id AND o.target_id=n.recipient_user_id::text
  WHERE n.workspace_id=$1 AND n.handover_id=a.handover_id AND n.status<>'cancelled'
  HAVING count(*)>0
 ) notifications ON true
 WHERE EXISTS(SELECT 1 FROM member m JOIN "user" owner_user ON owner_user.id=m.user_id WHERE m.workspace_id=$1 AND m.user_id=$2::uuid AND m.role='owner' AND owner_user.account_disabled_at IS NULL)`, ws, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var source string
		var s memberHandoverStatus
		if err = rows.Scan(&source, &s.Status, &s.Reason, &s.RecipientName, &s.AttemptedAt, &s.NotificationStatus); err != nil {
			return nil, err
		}
		result[source] = &s
	}
	return result, rows.Err()
}
