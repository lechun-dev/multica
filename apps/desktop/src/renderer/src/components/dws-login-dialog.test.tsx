import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DwsAuthRequirement } from "../../../shared/dws-auth";

const translations = {
  desktop: {
    dws_auth: {
      title: "连接钉钉",
      not_logged_in_title: "登录钉钉",
      identity_mismatch_title: "切换钉钉账号",
      not_installed_title: "安装 DWS",
      description: "DWS 当前未登录或登录已失效。",
      identity_mismatch: "DWS 已登录，但账号不一致。",
      not_installed: "未找到 DWS CLI。",
      browser_hint: "请在浏览器完成授权。",
      authorizing: "正在等待钉钉授权...",
      login: "去授权登录",
      reauthorize: "切换并重新授权",
      detect_and_login: "重新检测并连接",
      later: "稍后处理",
      success: "已连接。",
      failed: "授权未完成。",
    },
  },
};

const mocks = vi.hoisted(() => ({
  login: vi.fn(),
  success: vi.fn(),
}));

vi.mock("@multica/views/i18n", () => ({
  useT: () => ({
    t: (selector: (resources: typeof translations) => string) =>
      selector(translations),
  }),
}));
vi.mock("sonner", () => ({ toast: { success: mocks.success } }));

import { DwsLoginDialog } from "./dws-login-dialog";

describe("DwsLoginDialog", () => {
  let requireAuth: (requirement: DwsAuthRequirement) => void;
  let resolveAuth: () => void;

  beforeEach(() => {
    mocks.login.mockReset().mockResolvedValue({ ok: true });
    mocks.success.mockReset();
    Object.defineProperty(window, "dwsAPI", {
      configurable: true,
      value: {
        getAuthRequirement: vi.fn().mockResolvedValue(null),
        getAuthStatus: vi.fn(),
        ensureAuthenticated: vi.fn(),
        login: mocks.login,
        onAuthRequired: (listener: typeof requireAuth) => {
          requireAuth = listener;
          return vi.fn();
        },
        onAuthResolved: (listener: typeof resolveAuth) => {
          resolveAuth = listener;
          return vi.fn();
        },
      },
    });
  });

  it("opens the shared dialog only after an auth-required event", () => {
    render(<DwsLoginDialog />);
    expect(screen.queryByText("登录钉钉")).not.toBeInTheDocument();

    act(() =>
      requireAuth({
        reason: "not_logged_in",
        source: "dingtalk_personal_message",
      }),
    );

    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "登录钉钉" })).toBeInTheDocument();
    expect(screen.getByText("DWS 当前未登录或登录已失效。")).toBeInTheDocument();
  });

  it("starts DWS login from the primary action and closes on success", async () => {
    render(<DwsLoginDialog />);
    act(() =>
      requireAuth({ reason: "not_logged_in", source: "future_dws_action" }),
    );

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "去授权登录" }));
    });

    expect(mocks.login).toHaveBeenCalledOnce();
    expect(mocks.success).toHaveBeenCalledWith("已连接。");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("uses the account-switch action for an identity mismatch", () => {
    render(<DwsLoginDialog />);
    act(() =>
      requireAuth({
        reason: "identity_mismatch",
        source: "dingtalk_personal_message",
      }),
    );

    expect(screen.getByRole("heading", { name: "切换钉钉账号" })).toBeInTheDocument();
    expect(screen.getByText("DWS 已登录，但账号不一致。")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "切换并重新授权" }),
    ).toBeInTheDocument();
  });

  it("closes when the main process reports that auth is resolved", () => {
    render(<DwsLoginDialog />);
    act(() =>
      requireAuth({ reason: "not_logged_in", source: "future_dws_action" }),
    );
    act(() => resolveAuth());

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
