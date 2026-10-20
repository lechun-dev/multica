package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatAccessRequestText(t *testing.T) {
	event := AccessRequestNotification{
		ApplicantName: "汉晓", RoleName: "可查看", IssueIdentifier: "LC-1788",
		IssueTitle: "子任务测试", SourceURL: "https://app.example/lechun/issues/LC-1788",
		ActionURL: "https://app.example/lechun/issues/LC-1788?access_request=request-1",
	}
	want := "🔔 **任务权限申请**\n\n汉晓 申请「可查看」权限\n\n---\n\n来源： [LC-1788 · 子任务测试](https://app.example/lechun/issues/LC-1788)\n\n[查看并处理申请](https://app.example/lechun/issues/LC-1788?access_request=request-1)"
	if got := FormatAccessRequestText(event); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	event.Outcome = "批准"
	result := FormatAccessRequestText(event)
	if !strings.Contains(result, "任务权限申请结果") || !strings.Contains(result, "申请的「可查看」权限已批准") || !strings.Contains(result, "[打开任务]") {
		t.Fatalf("incorrect result copy: %s", result)
	}
}

func TestAccessRequestDisplayValuesCannotInjectMarkdown(t *testing.T) {
	event := AccessRequestNotification{ApplicantName: " 小[明](https://evil.example)\n**伪造** ", RoleName: "可管理", IssueIdentifier: "LC-1", IssueTitle: "title\n[伪链接](https://evil.example)"}
	got := FormatAccessRequestText(event)
	if strings.Contains(got, "[伪链接]") || strings.Contains(got, "[明]") || strings.Contains(got, "\n**伪造**") {
		t.Fatalf("unescaped or multiline display values: %q", got)
	}
	event.ApplicantName = " "
	if !strings.Contains(FormatAccessRequestText(event), "工作区成员 申请") {
		t.Fatal("missing safe name fallback")
	}
	if strings.Contains(got, "查看并处理申请") {
		t.Fatal("missing canonical app URL must not produce a broken action link")
	}
}

func TestAccessRequestRobotCardKeepsReviewActionAndTaskSource(t *testing.T) {
	for _, outcome := range []string{"", "批准", "拒绝"} {
		t.Run("outcome="+outcome, func(t *testing.T) {
			event := AccessRequestNotification{
				ApplicantName: "汉晓", RoleName: "可查看", IssueIdentifier: "LC-1788",
				IssueTitle: "子任务测试", SourceURL: "https://app.example/lechun/issues/LC-1788", Outcome: outcome,
			}
			event.ActionURL = event.SourceURL
			wantButton := "打开任务"
			if outcome == "" {
				event.ActionURL += "?access_request=request-1"
				wantButton = "查看并处理申请"
			}
			param, ok := mustActionCardParam(FormatAccessRequestText(event))
			if !ok {
				t.Fatal("permission notification must use the robot's action card")
			}
			var card map[string]string
			if err := json.Unmarshal([]byte(param), &card); err != nil {
				t.Fatal(err)
			}
			if card["singleTitle"] != wantButton || card["singleURL"] != event.ActionURL || !strings.Contains(card["text"], "来源： [LC-1788 · 子任务测试]("+event.SourceURL+")") {
				t.Fatalf("incorrect robot review card: %+v", card)
			}
		})
	}
}
