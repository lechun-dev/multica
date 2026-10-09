import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import type { WorkspaceRuntimeModel } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { RuntimeModelsTab } from "./runtime-models-tab";

const mocks = vi.hoisted(() => ({ update: vi.fn(), create: vi.fn(), remove: vi.fn(), error: vi.fn(), success: vi.fn() }));
const model: WorkspaceRuntimeModel = {
  id: "catalog-row-uuid", workspace_id: "workspace-1", runtime_provider: "codex",
  model_id: "deepseek-flash", display_name: "Flash", model_provider: "deepseek",
  description: "", thinking_levels: [], default_thinking_level: "", service_tiers: [],
  supports_explicit_standard_service_tier: false, enabled: true, sort_order: 0,
  created_at: "2026-10-09T00:00:00Z", updated_at: "2026-10-09T00:00:00Z",
};
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@tanstack/react-query", () => ({ useQuery: () => ({ data: [model], isLoading: false, isError: false }) }));
vi.mock("@multica/core/workspace-runtime-models", () => ({
  workspaceRuntimeModelListOptions: () => ({}),
  useUpdateWorkspaceRuntimeModel: () => ({ mutateAsync: mocks.update, isPending: false }),
  useCreateWorkspaceRuntimeModel: () => ({ mutateAsync: mocks.create, isPending: false }),
  useDeleteWorkspaceRuntimeModel: () => ({ mutateAsync: mocks.remove, isPending: false }),
}));
vi.mock("sonner", () => ({ toast: { error: mocks.error, success: mocks.success } }));

beforeEach(() => { vi.resetAllMocks(); mocks.update.mockResolvedValue(model); });
afterEach(cleanup);
function openEdit() {
  renderWithI18n(<RuntimeModelsTab />);
  fireEvent.click(screen.getByRole("button", { name: "Edit runtime model" }));
  return screen.getByLabelText("Model ID");
}

// 2026-10-09 coder(lq): CLI IDs are editable values; the request still addresses the same stable catalog row UUID.
describe("runtime model ID editing", () => {
  it("allows an opaque CLI ID to be edited and saved unchanged", async () => {
    const input = openEdit();
    expect(input).toBeEnabled();
    const id = '["deepseek-official","deepseek-flash"]';
    fireEvent.change(input, { target: { value: ` ${id} ` } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith(expect.objectContaining({ modelId: model.id, model_id: id, display_name: "Flash" })));
    expect(mocks.create).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
  it("rejects an empty ID without sending an update", () => {
    fireEvent.change(openEdit(), { target: { value: "   " } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.update).not.toHaveBeenCalled();
    expect(mocks.error).toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
  it("keeps the editor open when the server rejects a duplicate ID", async () => {
    mocks.update.mockRejectedValue(new Error("this model already exists for the runtime"));
    fireEvent.change(openEdit(), { target: { value: "already-configured" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("this model already exists for the runtime"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});
