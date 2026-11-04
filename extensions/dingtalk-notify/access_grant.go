package notify

import (
	"strings"
	"time"
)

type AccessGrantPermission struct {
	RoleName  string
	ExpiresAt *time.Time
}

type AccessGrantNotification struct {
	ActorName       string
	Updated         bool
	Permissions     []AccessGrantPermission
	IssueIdentifier string
	IssueTitle      string
	SourceURL       string
}

func FormatAccessGrantText(event AccessGrantNotification) string {
	name := strings.Join(strings.Fields(event.ActorName), " ")
	if name == "" {
		name = "工作区成员"
	}
	heading, action := "任务授权", "授予你以下任务权限"
	if event.Updated {
		heading, action = "任务授权调整", "调整了你的任务授权"
	}
	text := "🔔 **" + heading + "**\n\n" + escapeMarkdown(name) + action
	for _, permission := range event.Permissions {
		expiry := "长期"
		if permission.ExpiresAt != nil {
			expiry = permission.ExpiresAt.In(time.FixedZone("UTC+8", 8*60*60)).Format("2006-01-02 15:04:05 -07:00")
		}
		role := strings.Join(strings.Fields(permission.RoleName), " ")
		text += "\n\n权限：" + escapeMarkdown(role) + "\n\n有效期：" + expiry
	}
	source := notificationTask(MentionCreated{
		IssueIdentifier: strings.Join(strings.Fields(event.IssueIdentifier), " "),
		IssueTitle:      strings.Join(strings.Fields(event.IssueTitle), " "),
		SourceURL:       event.SourceURL,
	})
	if source != "" {
		text += "\n\n---\n\n来源： " + source
	}
	if event.SourceURL != "" {
		text += "\n\n[打开任务](" + event.SourceURL + ")"
	}
	return text
}
