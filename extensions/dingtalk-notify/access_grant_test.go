package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAccessGrantNotificationFormatting(t *testing.T) {
	expiry := time.Date(2026, 11, 5, 2, 30, 0, 0, time.UTC)
	event := AccessGrantNotification{ActorName: "张畅", IssueIdentifier: "LC-3070", IssueTitle: "任务分享",
		SourceURL:   "https://app.example/lechun/issues/LC-3070",
		Permissions: []AccessGrantPermission{{RoleName: "可编辑"}, {RoleName: "可查看", ExpiresAt: &expiry}}}
	got := FormatAccessGrantText(event)
	for _, part := range []string{"**任务授权**", "张畅授予你", "权限：可编辑", "有效期：长期", "2026-11-05 10:30:00 +08:00", "来源： [LC-3070 · 任务分享](" + event.SourceURL + ")"} {
		if !strings.Contains(got, part) {
			t.Errorf("missing %q: %s", part, got)
		}
	}
	param, ok := mustActionCardParam(got)
	var card map[string]string
	if !ok || json.Unmarshal([]byte(param), &card) != nil || card["singleTitle"] != "打开任务" || card["singleURL"] != event.SourceURL {
		t.Fatalf("incorrect grant action card: %s", param)
	}
	event.Updated = true
	event.ActorName = " 小[明](https://evil.example)\n**伪造** "
	event.IssueTitle = "title\n[伪链接](https://evil.example)"
	event.Permissions[0].RoleName = "role\n**注入**"
	event.SourceURL = ""
	got = FormatAccessGrantText(event)
	if !strings.Contains(got, "任务授权调整") || !strings.Contains(got, "调整了你的任务授权") {
		t.Fatalf("missing adjustment context: %s", got)
	}
	for _, bad := range []string{"[明]", "[伪链接]", "\n**伪造**", "**注入**", "[打开任务]"} {
		if strings.Contains(got, bad) {
			t.Errorf("unsafe display value or broken action %q: %s", bad, got)
		}
	}
}
