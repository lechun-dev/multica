import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "@multica/views/locales";
import { DshCliAction } from "./dsh-cli-dialog";
import type { DshCliStatus } from "../../../shared/dsh-cli";

const api = { getDshCliStatus: vi.fn<() => Promise<DshCliStatus>>() };
function show() {
  render(<I18nProvider locale="zh-Hans" resources={RESOURCES}><DshCliAction /></I18nProvider>);
  fireEvent.click(screen.getByRole("button", { name: "DSH CLI" }));
}
beforeEach(() => {
  vi.resetAllMocks(); api.getDshCliStatus.mockResolvedValue({ state: "not_installed" });
  Object.defineProperty(window, "daemonAPI", { configurable: true, value: api });
});

describe("DSH CLI runtime header dialog", () => {
  it("opens lazily and prompts official installation without offering global command repair", async () => {
    render(<I18nProvider locale="zh-Hans" resources={RESOURCES}><DshCliAction /></I18nProvider>);
    expect(api.getDshCliStatus).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "DSH CLI" }));
    expect(await screen.findByRole("dialog", { name: "DSH CLI" })).toBeInTheDocument();
    expect(await screen.findByText(/请先安装官方 DeepSeek Harness/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "安装／修复 CLI" })).not.toBeInTheDocument();
  });
  it("shows official ACP readiness after rechecking and disables pending controls", async () => {
    show();
    const button = await screen.findByRole("button", { name: "重新检测" });
    await screen.findByText(/请先安装官方/);
    let finish: (s: DshCliStatus) => void = () => {};
    api.getDshCliStatus.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    fireEvent.click(button);
    expect(screen.getByRole("button", { name: /检查中/ })).toBeDisabled();
    finish({ state: "ready" });
    expect(await screen.findByText(/DSH CLI 可用，已通过官方 ACP/)).toBeInTheDocument();
  });
  it.each([
    ["probe_timeout", /DSH 检测超时/], ["protocol_incompatible", /不支持兼容的 ACP/],
    ["launch_failed", /DSH 命令无法启动/], ["probe_failed", /未完成官方 ACP 握手/],
  ] as const)("renders actionable %s feedback", async (reason, text) => {
    api.getDshCliStatus.mockResolvedValue({ state: "error", reason }); show();
    expect(await screen.findByText(text)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重新检测" })).toBeEnabled();
  });
  it("handles IPC failure without displaying raw errors", async () => {
    api.getDshCliStatus.mockRejectedValue(new Error("SECRET_TEST_VALUE")); show();
    expect(await screen.findByText(/无法验证 DSH CLI/)).toBeInTheDocument();
    expect(screen.queryByText(/SECRET_TEST_VALUE/)).not.toBeInTheDocument();
  });
});
