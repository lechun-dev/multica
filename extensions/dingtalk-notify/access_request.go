package notify

import "strings"

// 2026-10-20 coder(lq): Keep access-request copy independent of database IDs
// and reuse task-source formatting from the existing DingTalk notifications.
type AccessRequestNotification struct {
	ApplicantName   string
	RoleName        string
	Outcome         string
	IssueIdentifier string
	IssueTitle      string
	SourceURL       string
	ActionURL       string
}

func AccessRequestSummary(event AccessRequestNotification) string {
	role := strings.Join(strings.Fields(event.RoleName), " ")
	if event.Outcome != "" {
		return "申请的「" + role + "」权限已" + event.Outcome
	}
	name := strings.Join(strings.Fields(event.ApplicantName), " ")
	if name == "" {
		name = "工作区成员"
	}
	return name + " 申请「" + role + "」权限"
}

func FormatAccessRequestText(event AccessRequestNotification) string {
	heading := "🔔 **任务权限申请**"
	action := "查看并处理申请"
	if event.Outcome != "" {
		heading = "🔔 **任务权限申请结果**"
		action = "打开任务"
	}
	text := heading + "\n\n" + escapeMarkdown(AccessRequestSummary(event))
	source := notificationTask(MentionCreated{
		IssueIdentifier: strings.Join(strings.Fields(event.IssueIdentifier), " "),
		IssueTitle:      strings.Join(strings.Fields(event.IssueTitle), " "),
		SourceURL:       event.SourceURL,
	})
	if source != "" {
		text += "\n\n---\n\n来源： " + source
	}
	if event.ActionURL != "" {
		text += "\n\n[" + action + "](" + event.ActionURL + ")"
	}
	return text
}
