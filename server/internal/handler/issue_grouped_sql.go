package handler

import "fmt"

// 2026-10-10 coder(lq): Rank only task IDs and group keys, then hydrate the
// selected page; large descriptions and properties never enter window sorts.
func groupedIssuePageSQL(where, order, offsetRef, limitRef, userRef string, includeOwned bool) string {
	visibility := ""
	if userRef != "" {
		visibility = issueVisibilityCandidateCTEDefs("$1", userRef, includeOwned, where) + ","
		where += " AND i.id IN (SELECT id FROM issue_auth_visible)"
	}
	return fmt.Sprintf(`WITH %s ranked AS (
	SELECT i.id, i.assignee_type, i.assignee_id,
	COUNT(*) OVER (PARTITION BY i.assignee_type, i.assignee_id) AS group_total,
	ROW_NUMBER() OVER (PARTITION BY i.assignee_type, i.assignee_id ORDER BY %s) AS rn
	FROM issue i WHERE %s
	), page AS MATERIALIZED (
	SELECT * FROM ranked WHERE rn > %s AND rn <= %s + %s
	)
	SELECT i.id, i.workspace_id, i.title, i.description, i.status, i.priority,
		i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
		i.parent_issue_id, i.position, i.start_date, i.due_date, i.created_at, i.updated_at, i.last_activity_at,
		i.number, i.project_id, i.metadata, i.stage, i.properties, i.revision, i.archived_at, p.group_total
	FROM page p JOIN issue i ON i.id=p.id AND i.workspace_id=$1
	ORDER BY CASE p.assignee_type WHEN 'member' THEN 0 WHEN 'agent' THEN 1 WHEN 'squad' THEN 2 ELSE 3 END,
	p.assignee_type NULLS LAST, p.assignee_id NULLS LAST, p.rn`, visibility, order, where, offsetRef, offsetRef, limitRef)
}
