package projectauth

// 2026-08-27 coder(lq): Map project roles to permissions. Service.Check keeps
// native workspace-owner semantics and excludes membership management from
// the otherwise-preserved workspace-admin bypass.
type Policy struct {
	roles map[ProjectRole]map[Permission]bool
}

func DefaultPolicy() Policy {
	return Policy{roles: map[ProjectRole]map[Permission]bool{
		ProjectOwner: {
			View: true, Edit: true, IssueCreate: true, IssueManage: true,
			AgentUse: true, MemberManage: true, SettingsManage: true,
		},
		ProjectManager: {
			View: true, Edit: true, IssueCreate: true, IssueManage: true,
			AgentUse: true,
		},
		ProjectMember: {View: true, IssueCreate: true, AgentUse: true},
		ProjectViewer: {View: true},
	}}
}

func (p Policy) Allows(role ProjectRole, permission Permission) bool {
	return p.roles[role][permission]
}

func validProjectRole(role ProjectRole) bool {
	switch role {
	case ProjectOwner, ProjectManager, ProjectMember, ProjectViewer:
		return true
	default:
		return false
	}
}
