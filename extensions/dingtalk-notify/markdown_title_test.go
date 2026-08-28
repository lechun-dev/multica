package notify

import "testing"

func TestMarkdownMessageTitleUsesReadableFirstLine(t *testing.T) {
	if got := markdownMessageTitle("🔔 张畅 在 Multica 中提到了你\n\n来源：乐纯工作区\n\n任务：[C-1 · 天气](https://example.com)"); got != "🔔 张畅 在 Multica 中提到了你" {
		t.Fatalf("markdown title = %q", got)
	}
	if got := markdownMessageTitle("\n\n消息："); got != "消息：" {
		t.Fatalf("markdown title should use the first non-empty line, got %q", got)
	}
}

func TestSplitMarkdownMessageSeparatesTitleFromBody(t *testing.T) {
	title, body := splitMarkdownMessage("\n🔔 **张畅 在 Multica 中提到了你**\n\n来源：乐纯工作区\n\n@张畅 请确认")
	if title != "🔔 张畅 在 Multica 中提到了你" {
		t.Fatalf("title = %q", title)
	}
	if body != "来源：乐纯工作区\n\n@张畅 请确认" {
		t.Fatalf("body = %q", body)
	}
}
