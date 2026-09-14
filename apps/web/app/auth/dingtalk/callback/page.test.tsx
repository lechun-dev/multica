import { render, screen, waitFor } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enAuth from "@multica/views/locales/en/auth.json";
import enCommon from "@multica/views/locales/en/common.json";
import enSettings from "@multica/views/locales/en/settings.json";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const {
  mockDingTalkLogin,
  mockEnsureQueryData,
  mockSetQueryData,
  mockRemoveQueries,
  mockLoginWithDingTalk,
  mockGetDingTalkDWSStatus,
  mockClearAuthorizationAttempt,
  mockPush,
  mockReplace,
  mockSearchParams,
} = vi.hoisted(() => ({
  mockDingTalkLogin: vi.fn(),
  mockEnsureQueryData: vi.fn(),
  mockSetQueryData: vi.fn(),
  mockRemoveQueries: vi.fn(),
  mockLoginWithDingTalk: vi.fn(),
  mockGetDingTalkDWSStatus: vi.fn(),
  mockClearAuthorizationAttempt: vi.fn(),
  mockPush: vi.fn(),
  mockReplace: vi.fn(),
  mockSearchParams: new URLSearchParams(),
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: mockPush, replace: mockReplace }),
  useSearchParams: () => mockSearchParams,
}));

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({
    ensureQueryData: mockEnsureQueryData,
    setQueryData: mockSetQueryData,
    removeQueries: mockRemoveQueries,
  }),
}));

vi.mock("@multica/core/auth", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/auth")>(
      "@multica/core/auth",
    );
  return {
    ...actual,
    useAuthStore: (selector: (state: unknown) => unknown) =>
      selector({ loginWithDingTalk: mockLoginWithDingTalk }),
  };
});

vi.mock("@multica/core/api", () => ({
  api: {
    dingTalkLogin: mockDingTalkLogin,
    getDingTalkDWSStatus: mockGetDingTalkDWSStatus,
  },
}));

vi.mock("@multica/core/workspace/queries", () => ({
  workspaceKeys: { list: () => ["workspaces"] },
  workspaceListOptions: () => ({ queryKey: ["workspaces"] }),
}));

vi.mock("@multica/views/dingtalk", () => ({
  dingtalkDWSStatusKey: ["me", "dingtalk-dws"],
  clearDingTalkDWSAuthorizationAttempt: mockClearAuthorizationAttempt,
}));

const TEST_RESOURCES = {
  en: { auth: enAuth, common: enCommon, settings: enSettings },
};

function Wrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

import CallbackPage from "./page";

describe("DingTalkCallbackPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    Array.from(mockSearchParams.keys()).forEach((key) =>
      mockSearchParams.delete(key),
    );
    mockSearchParams.set("code", "dingtalk-code");
    mockSearchParams.set("state", "trusted-random.desktop");
    mockGetDingTalkDWSStatus.mockResolvedValue({
      configured: true,
      connected: true,
      state: "connected",
      pending_count: 0,
    });
  });

  it("hands a successful desktop login back to the Multica app", async () => {
    mockDingTalkLogin.mockResolvedValue({ token: "desktop-jwt" });
    const hrefSetter = vi.fn();
    const originalLocation = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: {
        ...originalLocation,
        set href(value: string) {
          hrefSetter(value);
        },
      },
    });

    try {
      render(<CallbackPage />, { wrapper: Wrapper });

      await waitFor(() => {
        expect(mockDingTalkLogin).toHaveBeenCalledWith(
          "dingtalk-code",
          "trusted-random.desktop",
        );
      });
      await waitFor(() => {
        expect(hrefSetter).toHaveBeenCalledWith(
          "multica://auth/callback?token=desktop-jwt",
        );
      });
      expect(
        await screen.findByRole("button", { name: "Open Multica Desktop" }),
      ).toBeInTheDocument();
      expect(mockLoginWithDingTalk).not.toHaveBeenCalled();
      expect(mockGetDingTalkDWSStatus).not.toHaveBeenCalled();
    } finally {
      Object.defineProperty(window, "location", {
        configurable: true,
        value: originalLocation,
      });
    }
  });

  it("hands a successful local desktop login back through multica-dev", async () => {
    mockSearchParams.set("state", "trusted-random.desktop-dev");
    mockDingTalkLogin.mockResolvedValue({ token: "desktop-dev-jwt" });
    const hrefSetter = vi.fn();
    const originalLocation = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: {
        ...originalLocation,
        set href(value: string) {
          hrefSetter(value);
        },
      },
    });

    try {
      render(<CallbackPage />, { wrapper: Wrapper });

      await waitFor(() => {
        expect(hrefSetter).toHaveBeenCalledWith(
          "multica-dev://auth/callback?token=desktop-dev-jwt",
        );
      });
    } finally {
      Object.defineProperty(window, "location", {
        configurable: true,
        value: originalLocation,
      });
    }
  });

  it("hands a Lechun desktop login back through the Lechun protocol", async () => {
    mockSearchParams.set("code", "desktop-lechun-code");
    mockSearchParams.set("state", "trusted-random.desktop-lechun");
    mockDingTalkLogin.mockResolvedValue({ token: "desktop-lechun-jwt" });
    const hrefSetter = vi.fn();
    const originalLocation = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: {
        ...originalLocation,
        set href(value: string) {
          hrefSetter(value);
        },
      },
    });

    try {
      render(<CallbackPage />, { wrapper: Wrapper });

      await waitFor(() => {
        expect(hrefSetter).toHaveBeenCalledWith(
          "multica-lechun://auth/callback?token=desktop-lechun-jwt",
        );
      });
    } finally {
      Object.defineProperty(window, "location", {
        configurable: true,
        value: originalLocation,
      });
    }
  });

  it("returns a web login to the task and comment carried in state", async () => {
    const next = "/acme/issues/MUL-67#comment-comment-1";
    const encoded = btoa(next)
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
    mockSearchParams.set("state", `trusted-random.web.next.${encoded}`);
    mockLoginWithDingTalk.mockResolvedValue({ onboarded_at: "2026-01-01" });
    mockEnsureQueryData.mockResolvedValue([]);

    render(<CallbackPage />, { wrapper: Wrapper });

    await waitFor(() => {
      expect(mockReplace).toHaveBeenCalledWith(next);
    });
    expect(mockGetDingTalkDWSStatus).toHaveBeenCalledOnce();
    expect(mockSetQueryData).toHaveBeenCalledWith(
      ["me", "dingtalk-dws"],
      expect.objectContaining({ state: "connected" }),
    );
    expect(mockClearAuthorizationAttempt).toHaveBeenCalledOnce();
  });

  it("drops stale DWS status when the post-authorization refresh fails", async () => {
    const next = "/acme/issues/MUL-67";
    const encoded = btoa(next)
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
    mockSearchParams.set("state", `trusted-random.web.next.${encoded}`);
    mockLoginWithDingTalk.mockResolvedValue({ onboarded_at: "2026-01-01" });
    mockGetDingTalkDWSStatus.mockRejectedValue(new Error("temporary failure"));
    mockEnsureQueryData.mockResolvedValue([]);

    render(<CallbackPage />, { wrapper: Wrapper });

    await waitFor(() => {
      expect(mockReplace).toHaveBeenCalledWith(next);
    });
    expect(mockRemoveQueries).toHaveBeenCalledWith({
      queryKey: ["me", "dingtalk-dws"],
    });
    expect(mockClearAuthorizationAttempt).toHaveBeenCalledOnce();
  });
});
