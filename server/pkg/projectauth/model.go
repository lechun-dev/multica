package projectauth

// 2026-08-24 coder(lq): Keep workspace roles sourced from Multica's native member table.
type WorkspaceRole string

const (
	WorkspaceOwner  WorkspaceRole = "owner"
	WorkspaceAdmin  WorkspaceRole = "admin"
	WorkspaceMember WorkspaceRole = "member"
)

// 2026-08-24 coder(lq): Keep project roles independent from the native workspace role.
type ProjectRole string

const (
	ProjectOwner   ProjectRole = "owner"
	ProjectManager ProjectRole = "manager"
	ProjectMember  ProjectRole = "member"
	ProjectViewer  ProjectRole = "viewer"
)

// 2026-08-24 coder(lq): Carry the already-authenticated identity and native workspace
// membership. The HTTP adapter can construct it from Multica's request context.
type Subject struct {
	UserID        string
	WorkspaceID   string
	WorkspaceRole WorkspaceRole
}

// Organization is the provider-neutral directory snapshot used by project
// authorization. External providers (DingTalk, WeCom, Feishu, etc.) sync
// into this model; request-time authorization never calls those providers.
// 2026-09-01 coder(lq): Expose stable local organization IDs to authorization
// pickers while keeping provider-specific identifiers out of grant payloads.
type Organization struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Provider    string `json:"provider"`
	ExternalID  string `json:"external_id"`
	Name        string `json:"name"`
	ParentID    string `json:"parent_id,omitempty"`
	Status      string `json:"status"`
}

// 2026-08-31 coder(lq): Grant subjects are independent from external login
// providers. Adapters resolve external identities to the native user ID first.
type SubjectType string

const (
	SubjectUser         SubjectType = "user"
	SubjectRole         SubjectType = "role"
	SubjectOrganization SubjectType = "organization"
	SubjectEveryone     SubjectType = "everyone"
)

type GrantSource string

const (
	GrantSourceManual       GrantSource = "manual"
	GrantSourceOrganization GrantSource = "organization"
	GrantSourceEveryone     GrantSource = "everyone"
	GrantSourceMigration    GrantSource = "migration"
	GrantSourceSystem       GrantSource = "system"
)

// AccessGrant is the storage-neutral representation of one allow grant. A
// nil-equivalent empty IssueID means project scope; a value means task scope.
type AccessGrant struct {
	ID          string      `json:"id"`
	WorkspaceID string      `json:"workspace_id"`
	ProjectID   string      `json:"project_id"`
	IssueID     string      `json:"issue_id,omitempty"`
	SubjectType SubjectType `json:"subject_type"`
	SubjectID   string      `json:"subject_id,omitempty"`
	Role        ProjectRole `json:"role,omitempty"`
	Permission  Permission  `json:"permission,omitempty"`
	Source      GrantSource `json:"source"`
	GrantedBy   string      `json:"granted_by,omitempty"`
}

// 2026-08-24 coder(lq): Use strings so new permissions can be added
// without changing the storage schema or the native Multica models.
type Permission string

const (
	View           Permission = "project.view"
	Edit           Permission = "project.edit"
	IssueCreate    Permission = "project.issue.create"
	IssueManage    Permission = "project.issue.manage"
	AgentUse       Permission = "project.agent.use"
	MemberManage   Permission = "project.member.manage"
	SettingsManage Permission = "project.settings.manage"
)
