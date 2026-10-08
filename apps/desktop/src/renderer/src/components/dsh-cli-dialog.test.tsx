import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "@multica/views/locales";
import { DshCliAction } from "./dsh-cli-dialog";
import type { DshCliStatus } from "../../../shared/dsh-cli";

const api = {
  getDshCliStatus: vi.fn<() => Promise<DshCliStatus>>(),
  repairDshCli: vi.fn<() => Promise<DshCliStatus>>(),
};
function show() {
  const result = render(<I18nProvider locale="zh-Hans" resources={RESOURCES}><DshCliAction /></I18nProvider>);
  fireEvent.click(screen.getByRole("button", { name: "DSH CLI" }));
  return result;
}
beforeEach(() => {
  vi.resetAllMocks();
  api.getDshCliStatus.mockResolvedValue({ state: "not_installed" });
  api.repairDshCli.mockResolvedValue({ state: "ready" });
  Object.defineProperty(window, "daemonAPI", { configurable: true, value: api });
});

describe("DSH CLI runtime header dialog", () => {
  it("does not detect or repair until the header entry is opened", async () => {
    render(<I18nProvider locale="zh-Hans" resources={RESOURCES}><DshCliAction /></I18nProvider>);
    expect(api.getDshCliStatus).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "DSH CLI" }));
    expect(await screen.findByRole("dialog", { name: "DSH CLI" })).toBeInTheDocument();
    expect(await screen.findByText(/请先安装 DSH 桌面端/)).toBeInTheDocument();
    expect(api.getDshCliStatus).toHaveBeenCalledTimes(1);
    expect(api.repairDshCli).not.toHaveBeenCalled();
  });
  it("only prompts to install DSH when absent; it does not offer or execute repair", async () => {
    show();
    expect(await screen.findByText(/请先安装 DSH 桌面端/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "安装／修复 CLI" })).not.toBeInTheDocument();
    expect(api.repairDshCli).not.toHaveBeenCalled();
    api.getDshCliStatus.mockResolvedValue({ state: "needs_repair" });
    fireEvent.click(screen.getByRole("button", { name: "重新检测" }));
    expect(await screen.findByRole("button", { name: "安装／修复 CLI" })).toBeInTheDocument();
    expect(api.repairDshCli).not.toHaveBeenCalled();
  });

  it("repairs only on click, disables controls while pending, and renders the verified result", async () => {
    api.getDshCliStatus.mockResolvedValue({ state: "needs_repair" });
    let finish: (value: DshCliStatus) => void = () => {};
    api.repairDshCli.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    show();
    fireEvent.click(await screen.findByRole("button", { name: "安装／修复 CLI" }));
    expect(api.repairDshCli).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "安装／修复 CLI" })).toBeDisabled();
    expect(screen.getByRole("button", { name: /检查中/ })).toBeDisabled();
    finish({ state: "ready" });
    expect(await screen.findByText(/DSH CLI 可用/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "安装／修复 CLI" })).not.toBeInTheDocument();
  });

  it("shows errors instead of a successful registration on failed repair", async () => {
    api.getDshCliStatus.mockResolvedValue({ state: "needs_repair" });
    api.repairDshCli.mockRejectedValue(new Error("worker failed"));
    show();
    fireEvent.click(await screen.findByRole("button", { name: "安装／修复 CLI" }));
    expect(await screen.findByText(/无法验证 DSH CLI/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "重新检测" })).toBeEnabled());
  });

  it("distinguishes unsupported installed versions from missing apps", async () => {
    api.getDshCliStatus.mockResolvedValue({ state: "unsupported" });
    show();
    expect(await screen.findByText(/当前平台或 DSH 版本不支持 CLI 修复/)).toBeInTheDocument();
    expect(screen.queryByText(/请先安装 DSH 桌面端/)).not.toBeInTheDocument();
  });
});
