export interface TimelineInitialLanding {
  issueId: string;
  bottom: boolean | null;
}

/** Capture the first loaded timeline, including an empty one. Realtime
 * comments must not turn a title landing into a delayed bottom jump. */
export function resolveTimelineInitialLanding(
  previous: TimelineInitialLanding,
  issueId: string,
  ready: boolean,
  hasConversation: boolean,
): TimelineInitialLanding {
  const current = previous.issueId === issueId
    ? previous
    : { issueId, bottom: null };
  if (current.bottom !== null || !ready) return current;
  return { issueId, bottom: hasConversation };
}
