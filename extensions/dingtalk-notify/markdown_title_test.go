package notify

import "testing"

func TestMarkdownMessageTitleUsesReadableFirstLine(t *testing.T) {
	if got := markdownMessageTitle("🔔 **张畅提到了你**\n\n来源：乐纯工作区"); got != "🔔 张畅提到了你" {
		t.Fatalf("markdown title = %q", got)
	}
	if got := markdownMessageTitle("\n\n消息："); got != "消息：" {
		t.Fatalf("markdown title should use the first non-empty line, got %q", got)
	}
}
