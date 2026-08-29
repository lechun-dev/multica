import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";

const mocks = vi.hoisted(() => ({
  getPreferences: vi.fn(),
  setAutomaticUpdates: vi.fn(),
  checkForUpdates: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

const translations = {
  auto_save: { toast_saved: "Settings saved" },
  desktop: {
    updates: {
      title: "Updates",
      description: "Update preferences",
      current_version: "Current version",
      automatic_updates_title: "Automatic background updates",
      automatic_updates_description: "Download updates in the background",
      automatic_updates_save_failed: "Failed to save update settings",
      check_section_title: "Check for updates",
      check_section_description: "Check manually",
      up_to_date: "Up to date",
      downloading: "Downloading v{{version}}",
      downloaded: "Downloaded v{{version}}",
      check_now: "Check now",
      checking: "Checking",
    },
  },
};

vi.mock("@multica/views/i18n", () => ({
  useT: () => ({
    t: (
      selector: (resources: typeof translations) => string,
      values?: Record<string, string>,
    ) => {
      const template = selector(translations);
      return Object.entries(values ?? {}).reduce(
        (result, [key, value]) => result.replace(`{{${key}}}`, value),
        template,
      );
    },
  }),
}));

vi.mock("sonner", () => ({
  toast: {
    success: mocks.toastSuccess,
    error: mocks.toastError,
  },
}));

import { UpdatesSettingsTab } from "./updates-settings-tab";

describe("UpdatesSettingsTab", () => {
  beforeEach(() => {
    mocks.getPreferences.mockReset().mockResolvedValue({
      automaticUpdates: true,
    });
    mocks.setAutomaticUpdates.mockReset();
    mocks.checkForUpdates.mockReset();
    mocks.toastSuccess.mockReset();
    mocks.toastError.mockReset();

    Object.defineProperty(window, "desktopAPI", {
      configurable: true,
      value: { appInfo: { version: "1.2.3" } },
    });
    Object.defineProperty(window, "updater", {
      configurable: true,
      value: {
        getPreferences: mocks.getPreferences,
        setAutomaticUpdates: mocks.setAutomaticUpdates,
        checkForUpdates: mocks.checkForUpdates,
        onUpdateAvailable: (listener: (info: { version: string }) => void) => {
          updateAvailable = listener;
          return vi.fn();
        },
        onDownloadProgress: (listener: (progress: { percent: number }) => void) => {
          updateProgress = listener;
          return vi.fn();
        },
        onUpdateDownloaded: (listener: (info: { version: string }) => void) => {
          updateDownloaded = listener;
          return vi.fn();
        },
        onUpdateError: (listener: (error: { message: string }) => void) => {
          updateError = listener;
          return vi.fn();
        },
      },
    });
  });

  let updateAvailable: (info: { version: string }) => void;
  let updateProgress: (progress: { percent: number }) => void;
  let updateDownloaded: (info: { version: string }) => void;
  let updateError: (error: { message: string }) => void;

  it("loads the persisted preference and saves changes from the switch", async () => {
    mocks.getPreferences.mockResolvedValue({ automaticUpdates: false });
    mocks.setAutomaticUpdates.mockResolvedValue({ automaticUpdates: true });
    render(<UpdatesSettingsTab />);

    const toggle = screen.getByRole("switch", {
      name: "Automatic background updates",
    });
    // The switch renders as <span role="switch">, so jest-dom's toBeEnabled()
    // treats it as always enabled and does not actually wait for getPreferences
    // to resolve. Wait on the persisted value being reflected instead, which
    // deterministically holds until the loaded preference (false) is applied.
    await waitFor(() => expect(toggle).not.toBeChecked());

    fireEvent.click(toggle);

    await waitFor(() => {
      expect(mocks.setAutomaticUpdates).toHaveBeenCalledWith(true);
      expect(toggle).toBeChecked();
    });
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Settings saved", {
      id: "settings-auto-save",
    });
  });

  it("reflects background download completion and errors", async () => {
    render(<UpdatesSettingsTab />);
    await waitFor(() => expect(mocks.getPreferences).toHaveBeenCalled());

    act(() => updateAvailable({ version: "1.2.4" }));
    expect(screen.getByText("Downloading v1.2.4")).toBeInTheDocument();

    act(() => updateProgress({ percent: 37.4 }));
    expect(screen.getByText("(37%)")).toBeInTheDocument();

    act(() => updateDownloaded({ version: "1.2.4" }));
    expect(screen.getByText("Downloaded v1.2.4")).toBeInTheDocument();

    act(() => updateError({ message: "download timed out" }));
    expect(screen.getByText("download timed out")).toBeInTheDocument();
  });
});
