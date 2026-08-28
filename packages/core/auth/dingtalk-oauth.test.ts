// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  buildDingTalkLoginURL,
  dingtalkCallbackProtocol,
  isDesktopDingTalkState,
} from "./dingtalk-oauth";

describe("DingTalk OAuth helpers", () => {
  it("starts a desktop-aware flow on an absolute API origin", () => {
    expect(
      buildDingTalkLoginURL("https://multica.example.test", "desktop"),
    ).toBe(
      "https://multica.example.test/auth/dingtalk/start?client=desktop",
    );
  });

  it("marks local desktop flows separately from the production app", () => {
    expect(
      buildDingTalkLoginURL("https://multica.example.test", "desktop-dev"),
    ).toBe(
      "https://multica.example.test/auth/dingtalk/start?client=desktop-dev",
    );
  });

  it("uses the page origin for a relative or empty browser API base", () => {
    expect(
      buildDingTalkLoginURL("/api", "web", "https://app.example.test"),
    ).toBe("https://app.example.test/auth/dingtalk/start");
  });

  it("recognizes only the verified desktop state suffix", () => {
    expect(isDesktopDingTalkState("random.desktop")).toBe(true);
    expect(isDesktopDingTalkState("random.desktop-dev")).toBe(true);
    expect(isDesktopDingTalkState("random.web")).toBe(false);
    expect(isDesktopDingTalkState("desktop.random")).toBe(false);
    expect(dingtalkCallbackProtocol("random.desktop")).toBe("multica");
    expect(dingtalkCallbackProtocol("random.desktop-dev")).toBe("multica-dev");
  });
});
