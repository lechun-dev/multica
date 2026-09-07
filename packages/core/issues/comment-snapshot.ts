import type { Comment } from "../types";

// 2026-09-07 coder(lq): Comment mutation responses and workspace broadcasts
// may be partial during mixed-version deployments. Only complete snapshots
// are safe to render; callers should refetch the authoritative timeline when
// this predicate returns false.
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
