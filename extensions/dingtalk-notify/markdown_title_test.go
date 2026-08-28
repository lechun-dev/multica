package notify

import "testing"

func TestMarkdownMessageTitleUsesReadableFirstLine(t *testing.T) {
	if got := markdownMessageTitle("🔔 **张畅 在 Multica 中提到了你**\n\n***来源：乐纯工作区***\n\n***任务：[C-1 · 天气](https://example.com)***"); got != "🔔 张畅 在 Multica 中提到了你" {
		t.Fatalf("markdown title = %q", got)
	}
	if got := markdownMessageTitle("\n\n消息："); got != "消息：" {
		t.Fatalf("markdown title should use the first non-empty line, got %q", got)
	}
}
