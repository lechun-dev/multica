// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  buildDingTalkLoginURL,
  dingtalkCallbackProtocol,
  dingtalkNextFromState,
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

  it("marks Lechun desktop flows separately from the official app", () => {
    expect(
      buildDingTalkLoginURL("https://multica.example.test", "desktop-lechun"),
    ).toBe(
      "https://multica.example.test/auth/dingtalk/start?client=desktop-lechun",
    );
  });

  it("uses the page origin for a relative or empty browser API base", () => {
    expect(
      buildDingTalkLoginURL("/api", "web", "https://app.example.test"),
    ).toBe("https://app.example.test/auth/dingtalk/start");
  });

  it("carries a safe post-login path through the start URL", () => {
    expect(
      buildDingTalkLoginURL(
        "https://multica.example.test",
        "web",
        undefined,
        "/acme/issues/MUL-67#comment-comment-1",
      ),
    ).toBe(
      "https://multica.example.test/auth/dingtalk/start?next=%2Facme%2Fissues%2FMUL-67%23comment-comment-1",
    );
  });

  it("recognizes only the verified desktop state suffix", () => {
    expect(isDesktopDingTalkState("random.desktop")).toBe(true);
    expect(isDesktopDingTalkState("random.desktop-dev")).toBe(true);
    expect(isDesktopDingTalkState("random.desktop-lechun")).toBe(true);
    expect(isDesktopDingTalkState("random.web")).toBe(false);
    expect(isDesktopDingTalkState("desktop.random")).toBe(false);
    expect(dingtalkCallbackProtocol("random.desktop")).toBe("multica");
    expect(dingtalkCallbackProtocol("random.desktop-dev")).toBe("multica-dev");
    expect(dingtalkCallbackProtocol("random.desktop-lechun")).toBe("multica-lechun");
    expect(dingtalkCallbackProtocol("random.desktop.next.LQ")).toBe("multica");
  });

  it("decodes the optional post-login path from OAuth state", () => {
    const encoded = btoa("/acme/issues/MUL-67#comment-comment-1")
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
    expect(dingtalkNextFromState(`random.web.next.${encoded}`)).toBe(
      "/acme/issues/MUL-67#comment-comment-1",
    );
    expect(dingtalkNextFromState("random.web")).toBeNull();
  });
});
