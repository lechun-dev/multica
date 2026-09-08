import type { Comment, TimelineEntry } from "../types";

// 2026-09-08 coder(lq): Reject incomplete cached comments left by older clients
// before rendering. Empty text is valid for attachment-only comments.
export function isRenderableTimelineSnapshot(
  entry: TimelineEntry | null | undefined,
): entry is TimelineEntry {
  if (entry?.type === "activity") return true;

  return Boolean(
    entry?.type === "comment" &&
      entry.id &&
      entry.actor_type &&
      entry.actor_id &&
      typeof entry.created_at === "string" &&
      Number.isFinite(Date.parse(entry.created_at)) &&
      typeof entry.content === "string",
  );
}

// 2026-09-08 coder(lq): Workspace broadcasts intentionally omit protected
// comment content. Only complete snapshots are safe to render; callers must
// refetch the permission-checked timeline for metadata-only notifications.
export function isRenderableCommentSnapshot(
  comment: Partial<Comment> | null | undefined,
): comment is Comment {
  const hasValidDate = (value: unknown) =>
    typeof value === "string" &&
    value.length > 0 &&
    Number.isFinite(Date.parse(value));

  return Boolean(
    comment?.id &&
      comment.issue_id &&
      comment.author_type &&
      comment.author_id &&
      Object.prototype.hasOwnProperty.call(comment, "content") &&
      typeof comment.content === "string" &&
      Object.prototype.hasOwnProperty.call(comment, "parent_id") &&
      (comment.parent_id === null || typeof comment.parent_id === "string") &&
      comment.type &&
      hasValidDate(comment.created_at) &&
      hasValidDate(comment.updated_at),
  );
}
