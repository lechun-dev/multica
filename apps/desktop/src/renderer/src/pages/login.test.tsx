import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

vi.mock("@multica/views/auth", () => ({
  DingTalkFirstLoginFrame: ({ children }: { children: ReactNode }) => <>{children}</>,
  LoginPage: ({
    onDingTalkLogin,
  }: {
    onDingTalkLogin?: () => void;
  }) => <button onClick={onDingTalkLogin}>Continue with DingTalk</button>,
}));

vi.mock("@multica/views/platform", () => ({
  DragStrip: () => null,
}));

vi.mock("@multica/ui/components/common/multica-icon", () => ({
  MulticaIcon: (): ReactNode => null,
}));

import { DesktopLoginPage } from "./login";

describe("DesktopLoginPage", () => {
  const openExternal = vi.fn();

  beforeEach(() => {
    openExternal.mockReset().mockResolvedValue(undefined);
    Object.defineProperty(window, "desktopAPI", {
      configurable: true,
      value: {
        runtimeConfig: {
          ok: true,
          config: {
            schemaVersion: 1,
            apiUrl: "https://multica.example.test",
            wsUrl: "wss://multica.example.test/ws",
            appUrl: "https://multica.example.test",
          },
        },
        openExternal,
      },
    });
  });

  it("opens a desktop-aware DingTalk OAuth flow", () => {
    render(<DesktopLoginPage />);
    fireEvent.click(screen.getByRole("button", { name: "Continue with DingTalk" }));

    expect(openExternal).toHaveBeenCalledWith(
      "https://multica.example.test/auth/dingtalk/start?client=desktop-dev",
    );
  });
});
