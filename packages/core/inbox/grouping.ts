import type { InboxItem } from "../types/inbox";

// 2026-10-20 coder(lq): Access requests are independent actions, not task
// activity. Keep selection, dismissal and counts aligned with server groups.
export function inboxItemKey(item: Pick<InboxItem, "type" | "issue_id" | "id">): string {
  return item.type === "task_access_request" ? item.id : item.issue_id ?? item.id;
}
