import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";

const state = vi.hoisted(() => ({
  status: {
    configured: true,
    connected: false,
    state: "authorization_required",
    reason: "not_authorized",
    pending_count: 1,
  } as {
    configured: boolean;
    connected: boolean;
    state: string;
    reason?: string;
    message?: string;
    pending_count: number;
  },
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: state.status }),
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (value: { status: string }) => unknown) =>
    selector({ status: "authenticated" }),
}));

vi.mock("@multica/core/api", () => ({
  api: { startDingTalkDWSAuthorization: vi.fn() },
}));

import { DingTalkDWSAuthorizationDialog } from "./dws-authorization-dialog";

const resources = { en: { common: enCommon } };

function Wrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={resources}>
      {children}
    </I18nProvider>
  );
}

function renderDialog() {
  return render(
    <DingTalkDWSAuthorizationDialog
      client="web"
      openAuthorization={vi.fn()}
    />,
    { wrapper: Wrapper },
  );
}

describe("DingTalkDWSAuthorizationDialog", () => {
  beforeEach(() => {
    state.status = {
      configured: true,
      connected: false,
      state: "authorization_required",
      reason: "not_authorized",
      pending_count: 1,
    };
  });

  it("offers authorization only for an authorization failure", () => {
    renderDialog();

    expect(screen.getByText("Authorize DingTalk direct messages")).toBeInTheDocument();
    expect(screen.getByText("Authorize sign-in")).toBeInTheDocument();
  });

  it("shows a server configuration error without calling it logged out", () => {
    state.status = {
      configured: false,
      connected: false,
      state: "unavailable",
      reason: "server_not_configured",
      message: "The deployment encryption key is missing.",
      pending_count: 1,
    };
    renderDialog();

    expect(screen.getByText("DingTalk direct messages unavailable")).toBeInTheDocument();
    expect(screen.getByText("The deployment encryption key is missing.")).toBeInTheDocument();
    expect(screen.queryByText("Authorize sign-in")).toBeNull();
  });

  it("shows a permission failure as a delivery error", () => {
    state.status = {
      configured: true,
      connected: true,
      state: "delivery_error",
      reason: "permission_denied",
      message: "The DingTalk application is missing personal-message permission.",
      pending_count: 0,
    };
    renderDialog();

    expect(screen.getByText("DingTalk direct message failed")).toBeInTheDocument();
    expect(
      screen.getByText("The DingTalk application is missing personal-message permission."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Authorize sign-in")).toBeNull();
  });
});
