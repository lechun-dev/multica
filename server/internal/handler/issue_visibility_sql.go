package handler

import "fmt"

// 2026-09-14 coder(lq): The zero scope retains the standalone ACL predicates.
// Heavy reads reuse those same rules with statement-local materialized sets;
// standalone checks remain live; human list reads may bind cached organization IDs.
type issueVisibilitySQL struct {
	materialized         bool
	projectsMaterialized bool
}

// 2026-09-20 coder(lq): The table reads (rows/groups/facets) already open their
// own WITH list, so they need the definitions without a second WITH keyword.
// Keep this as the single source of the visibility set and let issueVisibilityCTEs
// wrap it for the callers that own the whole statement.
func issueVisibilityCTEDefs(workspaceRef, userRef string, includeWorkspaceOwned bool) string {
	return issueVisibilityCandidateCTEDefs(workspaceRef, userRef, includeWorkspaceOwned, "TRUE")
}

// 2026-10-09 coder(lq): Filter candidates before ACL evaluation, then include
// their direct parents without the list filters. Cross-project and archived
// parents still contribute Base, but grandparent inheritance never propagates.
func issueVisibilityCandidateCTEDefs(workspaceRef, userRef string, includeWorkspaceOwned bool, candidateWhere string) string {
	return issueVisibilityCandidateCTEDefsWithOrganizations(workspaceRef, userRef, includeWorkspaceOwned, candidateWhere, userOrganizationIDsSQL(workspaceRef, userRef))
}

// 2026-10-09 coder(lq): Only the organization source varies for cached reads;
// grants, roles, membership and direct-parent inheritance retain the same SQL.
func issueVisibilityCandidateCTEDefsWithOrganizations(workspaceRef, userRef string, includeWorkspaceOwned bool, candidateWhere, organizationSQL string) string {
	principals := issueVisibilitySQL{materialized: true}
	issues := issueVisibilitySQL{materialized: true, projectsMaterialized: true}
	ownerClause := "FALSE"
	if includeWorkspaceOwned {
		ownerClause = fmt.Sprintf("(%s AND EXISTS (SELECT 1 FROM member m WHERE m.workspace_id = %s AND m.user_id = %s::uuid AND m.role = 'owner'))", workspaceOwnerBypassPredicate(workspaceRef), workspaceRef, userRef)
	}
	baseCandidateWhere := "visible_issue.id IN (SELECT id FROM issue_auth_base_candidates)"
	projectCandidateWhere := "visible_project.id IN (SELECT bi.project_id FROM issue bi WHERE bi.id IN (SELECT id FROM issue_auth_base_candidates))"
	if candidateWhere == "TRUE" {
		baseCandidateWhere = "TRUE"
		projectCandidateWhere = "TRUE"
	}
	return fmt.Sprintf(`issue_auth_organizations(organization_id) AS MATERIALIZED (
		%s
	), issue_auth_candidates AS MATERIALIZED (
		SELECT i.id, i.parent_issue_id FROM issue i WHERE i.workspace_id = %s AND (%s)
	), issue_auth_base_candidates AS MATERIALIZED (
		SELECT id FROM issue_auth_candidates
		UNION
		SELECT p.id FROM issue p JOIN issue_auth_candidates c ON c.parent_issue_id = p.id
		WHERE p.workspace_id = %s
	), issue_auth_projects AS MATERIALIZED (
		SELECT visible_project.id FROM project visible_project
		WHERE visible_project.workspace_id = %s
		  AND (%s) AND %s
	), issue_auth_base AS MATERIALIZED (
		SELECT visible_issue.id FROM issue visible_issue
		WHERE visible_issue.workspace_id = %s AND (%s) AND %s
	), issue_auth_visible AS MATERIALIZED (
		SELECT c.id FROM issue_auth_candidates c
		WHERE %s OR c.id IN (SELECT id FROM issue_auth_base)
		  OR c.parent_issue_id IN (SELECT id FROM issue_auth_base)
	)
	`, organizationSQL, workspaceRef, candidateWhere,
		workspaceRef, workspaceRef, projectCandidateWhere, principals.projectAccess("visible_project.id", workspaceRef, userRef),
		workspaceRef, baseCandidateWhere, issues.base("visible_issue", workspaceRef, userRef), ownerClause)
}

func issueVisibilityCTEs(workspaceRef, userRef string, includeWorkspaceOwned bool) string {
	return "WITH " + issueVisibilityCTEDefs(workspaceRef, userRef, includeWorkspaceOwned)
}

// 2026-09-14 coder(lq): Match issue_effective_status exactly: canonical keys
// take precedence over catalog overrides, and unknown keys stay nonterminal.
func terminalIssueStatusSetSQL(workspaceRef string) string {
	return fmt.Sprintf(`SELECT key FROM (VALUES ('done'), ('cancelled')) canonical(key)
		UNION
		SELECT key FROM issue_status
		WHERE workspace_id = %s AND category IN ('done', 'closed')
		  AND key NOT IN ('backlog', 'todo', 'in_progress', 'in_review', 'done', 'blocked', 'cancelled')`, workspaceRef)
}

func childIssueProgressAuthorizedSQL(includeWorkspaceOwned bool, organizationSource ...string) string {
	// 2026-10-09 coder(lq): Standalone tasks cannot contribute progress; keep
	// only hierarchy participants while retaining the parents' own ACL sources.
	candidateWhere := "i.parent_issue_id IS NOT NULL OR i.id IN (SELECT child.parent_issue_id FROM issue child WHERE child.workspace_id = $1 AND child.parent_issue_id IS NOT NULL)"
	organizationSQL := userOrganizationIDsSQL("$1", "$2")
	if len(organizationSource) > 0 {
		organizationSQL = organizationSource[0]
	}
	return "WITH " + issueVisibilityCandidateCTEDefsWithOrganizations("$1", "$2", includeWorkspaceOwned, candidateWhere, organizationSQL) + fmt.Sprintf(`
	SELECT i.parent_issue_id,
		COUNT(*)::bigint AS total,
		COUNT(*) FILTER (WHERE i.status IN (%s))::bigint AS done
	FROM issue i
	JOIN issue_auth_visible child_visible ON child_visible.id = i.id
	JOIN issue_auth_visible parent_visible ON parent_visible.id = i.parent_issue_id
	WHERE i.workspace_id = $1
	GROUP BY i.parent_issue_id`, terminalIssueStatusSetSQL("$1"))
}
