import type { RuntimeModel, WorkspaceRuntimeModel } from "../types";

export type RuntimeCatalogModel = RuntimeModel & {
  catalogSource: "runtime" | "configured";
};

// 2026-10-09 coder(lq): System configuration is a shared candidate catalog,
// not proof of CLI/account support. Preserve opaque IDs and live capabilities.
export function mergeRuntimeModelCatalog(
  discovered: RuntimeModel[] | undefined,
  configured: WorkspaceRuntimeModel[] | undefined,
): RuntimeCatalogModel[] {
  const models = new Map<string, RuntimeCatalogModel>();
  for (const model of discovered ?? []) {
    if (!models.has(model.id)) models.set(model.id, { ...model, catalogSource: "runtime" });
  }
  for (const model of configured ?? []) {
    if (!model.enabled || models.has(model.model_id)) continue;
    models.set(model.model_id, {
      catalogSource: "configured",
      id: model.model_id,
      label: model.display_name,
      provider: model.model_provider,
      thinking:
        model.thinking_levels.length > 0
          ? {
              supported_levels: model.thinking_levels,
              default_level: model.default_thinking_level,
            }
          : undefined,
      service_tiers: model.service_tiers,
      supports_explicit_standard_service_tier:
        model.supports_explicit_standard_service_tier,
    });
  }
  return [...models.values()];
}
