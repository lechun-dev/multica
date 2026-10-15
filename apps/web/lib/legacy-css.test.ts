import { runInNewContext } from "node:vm";
import { describe, expect, it, vi } from "vitest";
import { LEGACY_CSS_BOOTSTRAP } from "./legacy-css";

function runBrowser(modern: boolean, flexGap = true) {
  document.head.innerHTML = "";
  document.body.innerHTML = "";
  document.documentElement.removeAttribute("data-legacy-css");
  document.documentElement.removeAttribute("data-legacy-flex-gap");
  document.documentElement.style.removeProperty("--legacy-viewport-height");
  const listeners = new Map<string, () => void>();
  const browser = {
    CSS: { supports: vi.fn(() => modern) },
    getComputedStyle: () => ({ getPropertyValue: () => modern ? "1" : "" }),
    innerHeight: 640,
    addEventListener: vi.fn((event, callback) => listeners.set(event, callback)),
  };
  const scrollHeight = vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(flexGap ? 3 : 2);
  const execute = () => runInNewContext(LEGACY_CSS_BOOTSTRAP, { document, window: browser });
  execute();
  scrollHeight.mockRestore();
  return { browser, listeners, execute };
}

describe("legacy CSS capability gate", () => {
  it("leaves modern browser styles, attributes and resize handling alone", () => {
    const { browser } = runBrowser(true);
    expect(document.head.querySelector("link")).toBeNull();
    expect(document.documentElement.hasAttribute("data-legacy-css")).toBe(false);
    expect(document.documentElement.style.getPropertyValue("--legacy-viewport-height")).toBe("");
    expect(browser.addEventListener).not.toHaveBeenCalled();
    expect(document.head.querySelector("style")).toBeNull();
  });
  it("loads once on old engines and measures flex gap rather than CSS.supports(gap)", () => {
    const { execute, listeners, browser } = runBrowser(false, false);
    execute();
    expect(document.head.querySelectorAll("#multica-legacy-css")).toHaveLength(1);
    expect(document.documentElement.hasAttribute("data-legacy-flex-gap")).toBe(true);
    browser.innerHeight = 420;
    listeners.get("resize")?.();
    expect(document.documentElement.style.getPropertyValue("--legacy-viewport-height")).toBe("420px");
    expect(document.body.children).toHaveLength(0);
  });
  it("does not add margin fallbacks when flex gap actually works", () => {
    runBrowser(false, true);
    expect(document.documentElement.hasAttribute("data-legacy-css")).toBe(true);
    expect(document.documentElement.hasAttribute("data-legacy-flex-gap")).toBe(false);
  });
});
