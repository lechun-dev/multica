import { useState } from "react";
import { act, fireEvent, render, cleanup } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useIssueDetailScrollRestore } from "./use-issue-detail-scroll-restore";

let frames = new Map<number, FrameRequestCallback>();
let frameId = 0;
let resize: () => void;
const disconnect = vi.fn();

function Harness({ restoreKey = "issue-a", ready = true, disabled = false, latest = true, overrideTop }: {
  restoreKey?: string; ready?: boolean; disabled?: boolean; latest?: boolean; overrideTop?: number;
}) {
  const [scrollContainerEl, setScrollContainerEl] = useState<HTMLDivElement | null>(null);
  useIssueDetailScrollRestore({ restoreKey, scrollContainerEl, ready, disabled, latest, overrideTop });
  return <div ref={setScrollContainerEl} data-testid="scroller"><div /></div>;
}
function flushFrame() {
  act(() => {
    const pending = [...frames.values()];
    frames.clear();
    pending.forEach((callback) => callback(performance.now()));
  });
}
function setHeight(el: HTMLElement, height: number) {
  Object.defineProperty(el, "scrollHeight", { configurable: true, value: height });
  Object.defineProperty(el, "clientHeight", { configurable: true, value: 100 });
  act(() => resize());
  flushFrame();
}

describe("initial issue positioning", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    frames = new Map();
    disconnect.mockClear();
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      frames.set(++frameId, callback);
      return frameId;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
    vi.stubGlobal("ResizeObserver", class {
      constructor(callback: () => void) { resize = callback; }
      observe() {}
      disconnect = disconnect;
    });
  });
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("lands at the latest conversation and adjusts for initial async height changes", () => {
    const { getByTestId } = render(<Harness />);
    const el = getByTestId("scroller");
    setHeight(el, 1000);
    expect(el.scrollTop).toBe(900);
    setHeight(el, 1800);
    expect(el.scrollTop).toBe(1700);
    el.scrollTop = 0; // Virtualizer's own initialization can reset it.
    flushFrame();
    expect(el.scrollTop).toBe(1700);
  });

  it("keeps tasks without a conversation at the title", () => {
    const { getByTestId } = render(<Harness latest={false} />);
    const el = getByTestId("scroller");
    expect(el.scrollTop).toBe(0);
  });

  it("waits for initial data", () => {
    const { getByTestId, rerender } = render(<Harness ready={false} />);
    const el = getByTestId("scroller");
    Object.defineProperty(el, "scrollHeight", { value: 1000 });
    Object.defineProperty(el, "clientHeight", { value: 100 });
    expect(el.scrollTop).toBe(0);
    rerender(<Harness />);
    expect(el.scrollTop).toBe(900);
  });

  it("restores a desktop tab memento instead of jumping to latest", () => {
    const { getByTestId } = render(<Harness overrideTop={340} />);
    const el = getByTestId("scroller");
    setHeight(el, 1000);
    expect(el.scrollTop).toBe(340);
  });

  it("respects a desktop memento at zero", () => {
    const { getByTestId } = render(<Harness overrideTop={0} />);
    expect(getByTestId("scroller").scrollTop).toBe(0);
  });

  it("ordinary re-entry starts at latest rather than a previous visit's offset", () => {
    const first = render(<Harness />);
    first.getByTestId("scroller").scrollTop = 200;
    first.unmount();
    const returned = render(<Harness />);
    const el = returned.getByTestId("scroller");
    setHeight(el, 1000);
    expect(el.scrollTop).toBe(900);
  });

  it.each(["wheel", "touchstart", "pointerdown", "keydown"])("yields immediately to %s", (event) => {
    const { getByTestId } = render(<Harness />);
    const el = getByTestId("scroller");
    setHeight(el, 1000);
    fireEvent(el, new Event(event));
    el.scrollTop = 200;
    setHeight(el, 2000);
    expect(el.scrollTop).toBe(200);
    expect(disconnect).toHaveBeenCalled();
  });

  it("does not follow subsequent messages after initial layout settles", () => {
    const { getByTestId, rerender } = render(<Harness />);
    const el = getByTestId("scroller");
    setHeight(el, 1000);
    act(() => vi.advanceTimersByTime(300));
    el.scrollTop = 250;
    setHeight(el, 2000);
    rerender(<Harness />);
    expect(el.scrollTop).toBe(250);
  });

  it("bounds positioning even when content keeps resizing", () => {
    const { getByTestId } = render(<Harness />);
    const el = getByTestId("scroller");
    for (let i = 0; i < 8; i++) {
      setHeight(el, 1000 + i * 100);
      act(() => vi.advanceTimersByTime(200));
    }
    el.scrollTop = 120;
    setHeight(el, 3000);
    expect(el.scrollTop).toBe(120);
  });

  it("leaves deep-link positioning alone after the highlight clears", () => {
    const { getByTestId, rerender } = render(<Harness disabled />);
    const el = getByTestId("scroller");
    el.scrollTop = 480;
    rerender(<Harness />);
    flushFrame();
    expect(el.scrollTop).toBe(480);
  });

  it("cancels pending work when navigating to another task", () => {
    const { getByTestId, rerender } = render(<Harness overrideTop={700} />);
    const el = getByTestId("scroller");
    rerender(<Harness restoreKey="issue-b" latest={false} />);
    flushFrame();
    expect(el.scrollTop).toBe(0);
    expect(disconnect).toHaveBeenCalled();
  });
});
