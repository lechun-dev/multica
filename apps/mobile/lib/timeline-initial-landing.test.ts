// @vitest-environment node
import { describe, expect, it } from "vitest";
import { resolveTimelineInitialLanding as resolve, type TimelineInitialLanding } from "./timeline-initial-landing";

describe("timeline initial landing", () => {
  const pending: TimelineInitialLanding = { issueId: "one", bottom: null };

  it("waits for real data before choosing the latest conversation", () => {
    expect(resolve(pending, "one", false, false)).toBe(pending);
    expect(resolve(pending, "one", true, true).bottom).toBe(true);
  });

  it.each([false, true])("preserves the first landing (%s) through refresh and realtime appends", (bottom) => {
    const loaded = resolve(pending, "one", true, bottom);
    expect(resolve(loaded, "one", false, false)).toBe(loaded);
    expect(resolve(loaded, "one", true, !bottom)).toBe(loaded);
  });

  it("captures an empty timeline so the first realtime comment cannot drag the reader down", () => {
    const empty = resolve(pending, "one", true, false);
    expect(resolve(empty, "one", true, true).bottom).toBe(false);
  });

  it("chooses independently when navigating to another issue", () => {
    const loaded = resolve(pending, "one", true, true);
    const next = resolve(loaded, "two", false, false);
    expect(next).toEqual({ issueId: "two", bottom: null });
    expect(resolve(next, "two", true, false).bottom).toBe(false);
  });
});
