import type {
  RuntimeModelServiceTier,
  RuntimeModelThinkingLevel,
} from "./agent";

export interface WorkspaceRuntimeModel {
  id: string;
  workspace_id: string;
  runtime_provider: string;
  model_id: string;
  display_name: string;
  model_provider: string;
  description: string;
  thinking_levels: RuntimeModelThinkingLevel[];
  default_thinking_level: string;
  service_tiers: RuntimeModelServiceTier[];
  supports_explicit_standard_service_tier: boolean;
  enabled: boolean;
  sort_order: number;
  created_at: string;
  updated_at: string;
}

export interface WorkspaceRuntimeModelRequest {
  runtime_provider?: string;
  model_id: string;
  display_name: string;
  model_provider?: string;
  description?: string;
  thinking_levels?: RuntimeModelThinkingLevel[];
  default_thinking_level?: string;
  service_tiers?: RuntimeModelServiceTier[];
  supports_explicit_standard_service_tier?: boolean;
  enabled?: boolean;
  sort_order?: number;
}

export type UpdateWorkspaceRuntimeModelRequest = Partial<
  Omit<WorkspaceRuntimeModelRequest, "runtime_provider" | "model_id">
>;
