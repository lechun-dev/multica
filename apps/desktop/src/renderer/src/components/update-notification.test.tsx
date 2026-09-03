import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { UpdateNotification } from "./update-notification";
import { DESKTOP_PRODUCT_NAME } from "../desktop-brand";

const mocks = vi.hoisted(() => ({
  installUpdate: vi.fn(),
}));

type UpdateDownloadedListener = (info: {
  version: string;
  releaseNotes?: string;
}) => void;
type UpdateErrorListener = (error: { message: string }) => void;

describe("UpdateNotification", () => {
  let updateDownloaded: UpdateDownloadedListener;
  let updateError: UpdateErrorListener;

  beforeEach(() => {
    mocks.installUpdate.mockReset().mockResolvedValue({ success: true });
    Object.defineProperty(window, "updater", {
      configurable: true,
      value: {
        onUpdateDownloaded: (listener: UpdateDownloadedListener) => {
          updateDownloaded = listener;
          return vi.fn();
        },
        onUpdateError: (listener: UpdateErrorListener) => {
          updateError = listener;
          return vi.fn();
        },
        installUpdate: mocks.installUpdate,
      },
    });
  });

  it("does not show a changelog link in the update prompt", () => {
    render(<UpdateNotification />);
    act(() => updateDownloaded({ version: "0.4.27" }));

    expect(
      screen.getByText(`${DESKTOP_PRODUCT_NAME} Update ready`),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "See changelog" }),
    ).not.toBeInTheDocument();
  });

  it("installs the update immediately from the primary action", async () => {
    render(<UpdateNotification />);
    act(() => updateDownloaded({ version: "0.4.27" }));

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Restart now" }));
    });

    expect(mocks.installUpdate).toHaveBeenCalledOnce();
  });

  it("shows an installation error instead of silently doing nothing", async () => {
    mocks.installUpdate.mockResolvedValue({
      success: false,
      error: "installer is unavailable",
    });
    render(<UpdateNotification />);
    act(() => updateDownloaded({ version: "0.4.27" }));

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Restart now" }));
    });

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Update failed: installer is unavailable",
    );
    expect(screen.getByRole("button", { name: "Restart now" })).toBeEnabled();
  });

  it("keeps background updater errors quiet", () => {
    render(<UpdateNotification />);
    act(() => updateDownloaded({ version: "0.4.27" }));
    act(() => updateError({ message: "downloaded package is missing" }));

    expect(
      screen.queryByText(`${DESKTOP_PRODUCT_NAME} Update failed`),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(
      screen.getByText(`${DESKTOP_PRODUCT_NAME} Update ready`),
    ).toBeInTheDocument();
  });

  it("does not show a prompt when a background update check cannot reach the server", () => {
    render(<UpdateNotification />);
    act(() =>
      updateError({
        message: "Unable to reach the update server. We’ll retry automatically.",
      }),
    );

    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Restart now" }),
    ).not.toBeInTheDocument();
  });
});
