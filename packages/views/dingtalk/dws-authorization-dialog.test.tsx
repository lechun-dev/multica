import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
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
  queryOptions: null as Record<string, unknown> | null,
  startAuthorization: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: Record<string, unknown>) => {
    state.queryOptions = options;
    return { data: state.status };
  },
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (value: { status: string }) => unknown) =>
    selector({ status: "authenticated" }),
}));

vi.mock("@multica/core/api", () => ({
  api: { startDingTalkDWSAuthorization: state.startAuthorization },
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

function renderDialog(openAuthorization = vi.fn()) {
  return render(
    <DingTalkDWSAuthorizationDialog
      client="web"
      openAuthorization={openAuthorization}
    />,
    { wrapper: Wrapper },
  );
}

describe("DingTalkDWSAuthorizationDialog", () => {
  beforeEach(() => {
    window.sessionStorage.clear();
    state.queryOptions = null;
    state.startAuthorization.mockReset();
    state.startAuthorization.mockResolvedValue({
      authorization_url: "https://login.dingtalk.test/oauth",
    });
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

  it("keeps the action disabled while authorization is pending", async () => {
    const openAuthorization = vi.fn().mockResolvedValue(undefined);
    renderDialog(openAuthorization);

    fireEvent.click(
      screen.getByRole("button", { name: "Authorize sign-in" }),
    );

    const waitingButton = await screen.findByRole("button", {
      name: "Authorizing, please wait",
    });
    expect(waitingButton).toBeDisabled();
    expect(screen.getByRole("button", { name: "Later" })).toBeDisabled();
    expect(openAuthorization).toHaveBeenCalledWith(
      "https://login.dingtalk.test/oauth",
    );
    expect(state.queryOptions?.refetchInterval).toBe(2_000);
    expect(state.queryOptions?.refetchIntervalInBackground).toBe(true);
  });

  it("restores the waiting state after the OAuth round trip", async () => {
    const first = renderDialog(vi.fn().mockResolvedValue(undefined));
    fireEvent.click(
      screen.getByRole("button", { name: "Authorize sign-in" }),
    );
    await screen.findByRole("button", {
      name: "Authorizing, please wait",
    });
    first.unmount();

    renderDialog();

    expect(
      await screen.findByRole("button", {
        name: "Authorizing, please wait",
      }),
    ).toBeDisabled();
  });

  it("closes automatically after the server reports a connected grant", async () => {
    const view = renderDialog(vi.fn().mockResolvedValue(undefined));
    fireEvent.click(
      screen.getByRole("button", { name: "Authorize sign-in" }),
    );
    await screen.findByRole("button", {
      name: "Authorizing, please wait",
    });

    state.status = {
      configured: true,
      connected: true,
      state: "connected",
      pending_count: 0,
    };
    view.rerender(
      <DingTalkDWSAuthorizationDialog
        client="web"
        openAuthorization={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(
        screen.queryByText("Authorize DingTalk direct messages"),
      ).toBeNull();
    });
    expect(window.sessionStorage.length).toBe(0);
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
