package handler

import "fmt"

// 2026-10-10 coder(lq): Keep raw notification counts while evaluating each
// candidate task's access once; system notices never require a task grant.
func unreadInboxCountSQL(window, permissions, includeOwned bool) string {
	filter := "i.workspace_id=$1 AND i.recipient_type='member' AND i.recipient_id=$2 AND i.read=false AND i.archived=false AND " + inboxIssueNotArchivedPredicate("i")
	if window {
		filter += " AND (i.issue_id IS NULL OR " + issueWindowIDPredicate("i.issue_id", "$1", "$3") + ")"
	}
	if !permissions {
		return "SELECT count(*)::bigint FROM inbox_item i WHERE " + filter
	}
	return "WITH notices AS MATERIALIZED (SELECT i.issue_id FROM inbox_item i WHERE " + filter + ")," +
		issueVisibilityCandidateCTEDefs("$1", "$2", includeOwned, "i.id IN (SELECT issue_id FROM notices)") +
		" SELECT count(*)::bigint FROM notices n WHERE n.issue_id IS NULL OR n.issue_id IN (SELECT id FROM issue_auth_visible)"
}

// 2026-10-10 coder(lq): Select the latest notice before testing unread state,
// so a newer read notice continues to suppress historical unread notices.
func unreadInboxWindowSummarySQL(permissions, includeOwned bool) string {
	visibility := ""
	predicate := ""
	if permissions {
		visibility = "," + issueVisibilityCandidateCTEDefs("policy.workspace_id", "$3", includeOwned, "i.id IN (SELECT issue_id FROM newest WHERE read=false)")
		predicate = " AND (newest.issue_id IS NULL OR newest.issue_id IN (SELECT id FROM issue_auth_visible))"
	}
	return fmt.Sprintf(`WITH policies AS (
	SELECT workspace_id, issue_limit FROM unnest($1::uuid[], $2::bigint[]) AS policy(workspace_id, issue_limit)
	) SELECT policy.workspace_id, filtered.count FROM policies policy CROSS JOIN LATERAL (
	WITH newest AS MATERIALIZED (
	SELECT DISTINCT ON (COALESCE(i.issue_id, i.id)) i.issue_id, i.read
	FROM inbox_item i JOIN member m ON m.workspace_id=i.workspace_id AND m.user_id=i.recipient_id
	WHERE i.workspace_id=policy.workspace_id AND i.recipient_type='member' AND i.recipient_id=$3 AND i.archived=false
	AND %s AND (i.issue_id IS NULL OR %s)
	ORDER BY COALESCE(i.issue_id, i.id), i.created_at DESC
	)%s SELECT count(*)::bigint AS count FROM newest WHERE newest.read=false%s
	) filtered`, inboxIssueNotArchivedPredicate("i"), issueWindowIDPredicate("i.issue_id", "policy.workspace_id", "policy.issue_limit"), visibility, predicate)
}
