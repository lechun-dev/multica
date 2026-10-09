// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { WorkspaceRuntimeModel } from "../types";
import { mergeRuntimeModelCatalog } from "./model-catalog";

const configured: WorkspaceRuntimeModel = {
  id: "configured", workspace_id: "ws", runtime_provider: "codex",
  model_id: "shared-model", display_name: "Configured model", model_provider: "gateway",
  description: "", thinking_levels: [{ value: "high", label: "High" }],
  default_thinking_level: "high", service_tiers: [],
  supports_explicit_standard_service_tier: false, enabled: true, sort_order: 0,
  created_at: "", updated_at: "",
};

describe("mergeRuntimeModelCatalog", () => {
  it("offers enabled system models without a CLI catalog", () => {
    expect(mergeRuntimeModelCatalog(undefined, [configured])).toEqual([
      expect.objectContaining({ id: "shared-model", label: "Configured model", thinking: { supported_levels: configured.thinking_levels, default_level: "high" } }),
    ]);
  });
  it("deduplicates exact IDs, preserves live metadata, and excludes disabled configuration", () => {
    const live = { id: "shared-model", label: "CLI label", default: true };
    expect(mergeRuntimeModelCatalog([live, live, { id: "live-only", label: "Live" }], [configured, { ...configured, model_id: "disabled", enabled: false }])).toEqual([{ ...live, catalogSource: "runtime" }, { id: "live-only", label: "Live", catalogSource: "runtime" }]);
  });
  it("preserves opaque CLI IDs and does not mutate either input", () => {
    const opaque = { ...configured, model_id: '["deepseek-official","deepseek-flash"]' };
    const result = mergeRuntimeModelCatalog([], [opaque]);
    expect(result[0]?.id).toBe(opaque.model_id);
    expect(result[0]?.catalogSource).toBe("configured");
    expect(opaque).toEqual({ ...configured, model_id: '["deepseek-official","deepseek-flash"]' });
  });
});
