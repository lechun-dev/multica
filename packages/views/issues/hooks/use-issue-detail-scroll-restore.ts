import { useLayoutEffect, useRef } from "react";

type UseIssueDetailScrollRestoreArgs = {
  restoreKey: string;
  scrollContainerEl: HTMLElement | null;
  ready: boolean;
  disabled?: boolean;
  latest?: boolean;
  /** Only desktop tab mementos restore a reading position across mounts. */
  overrideTop?: number;
};

export function useIssueDetailScrollRestore({
  restoreKey,
  scrollContainerEl,
  ready,
  disabled = false,
  latest = false,
  overrideTop,
}: UseIssueDetailScrollRestoreArgs) {
  const restoredKeyRef = useRef<string | null>(null);

  useLayoutEffect(() => {
    restoredKeyRef.current = null;
  }, [restoreKey]);

  useLayoutEffect(() => {
    if (!scrollContainerEl || !ready) return;
    if (disabled) {
      // A deep link owns this entry, including after its highlight clears.
      restoredKeyRef.current = restoreKey;
      return;
    }
    if (restoredKeyRef.current === restoreKey) return;
    restoredKeyRef.current = restoreKey;

    const el = scrollContainerEl;
    const bottom = overrideTop === undefined && latest;
    const target = () => bottom ? Math.max(0, el.scrollHeight - el.clientHeight) : (overrideTop ?? 0);
    if (!bottom && target() <= 1) {
      el.scrollTop = target();
      return;
    }

    // Initial positioning only. Allow virtual rows / markdown / images to
    // settle briefly, then relinquish scroll ownership. Never follow output.
    let frameId = 0;
    let quietTimer: ReturnType<typeof setTimeout>;
    let stopped = false;
    const stop = () => {
      stopped = true;
      cancelAnimationFrame(frameId);
      clearTimeout(quietTimer);
      clearTimeout(deadline);
      observer?.disconnect();
      for (const event of inputEvents) el.removeEventListener(event, stop);
      el.removeEventListener("load", schedule, true);
    };
    const position = () => {
      if (!stopped && el.isConnected) el.scrollTop = target();
    };
    const schedule = () => {
      if (stopped) return;
      clearTimeout(quietTimer);
      cancelAnimationFrame(frameId);
      frameId = requestAnimationFrame(() => {
        position();
        // A second frame outlasts Virtuoso's own initial scroll write.
        frameId = requestAnimationFrame(position);
      });
      quietTimer = setTimeout(stop, 250);
    };
    const inputEvents = ["wheel", "touchstart", "pointerdown", "keydown"];
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(schedule);
    const deadline = setTimeout(stop, 1500);
    for (const child of el.children) observer?.observe(child);
    for (const event of inputEvents) el.addEventListener(event, stop, { passive: true });
    el.addEventListener("load", schedule, true);
    position();
    schedule();
    return stop;
  }, [scrollContainerEl, restoreKey, ready, disabled, overrideTop, latest]);
}
