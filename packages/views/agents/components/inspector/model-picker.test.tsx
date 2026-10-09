// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { RuntimeModelListRequest, WorkspaceRuntimeModel } from "@multica/core/types";
import { afterEach, describe, expect, it, vi } from "vitest";
import enAgents from "../../../locales/en/agents.json";
import enCommon from "../../../locales/en/common.json";
import enIssues from "../../../locales/en/issues.json";
import { ModelPicker } from "./model-picker";

let response: RuntimeModelListRequest;
let configuration: WorkspaceRuntimeModel[] = [];
let pending = false;
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("@multica/core/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("@multica/core/api")>(),
  api: { initiateListModels: () => pending ? new Promise(() => {}) : Promise.resolve(response), listWorkspaceRuntimeModels: async () => configuration },
}));

function renderPicker(value = "", runtimeOnline = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onChange = vi.fn();
  render(
    <I18nProvider locale="en" resources={{ en: { agents: enAgents, common: enCommon, issues: enIssues } }}>
      <QueryClientProvider client={client}>
        <ModelPicker runtimeId="dsh-runtime" runtimeOnline={runtimeOnline} value={value} onChange={onChange} />
      </QueryClientProvider>
    </I18nProvider>,
  );
  fireEvent.click(screen.getByRole("button", { name: /^Model · / }));
  return onChange;
}

function catalog(fields: Partial<RuntimeModelListRequest> = {}): RuntimeModelListRequest {
  return {
    id: "model-request",
    runtime_id: "dsh-runtime",
    status: "completed",
    supported: true,
    models: [],
    created_at: "2026-10-09T00:00:00Z",
    updated_at: "2026-10-09T00:00:00Z",
    ...fields,
  };
}

describe("ModelPicker discovery", () => {
  it.each(["pending", "failed", "empty", "offline"])("offers the shared system catalog when discovery is %s", async (state) => {
    configuration = [{
      id: "config", workspace_id: "ws", runtime_provider: "codex", model_id: "configured-model",
      display_name: "Configured system model", model_provider: "gateway", description: "", thinking_levels: [],
      default_thinking_level: "", service_tiers: [], supports_explicit_standard_service_tier: false,
      enabled: true, sort_order: 0, created_at: "", updated_at: "",
    }];
    pending = state === "pending";
    response = catalog(state === "failed" ? { status: "failed", error: "CLI failed" } : {});
    const onChange = renderPicker("", state !== "offline");
    expect(await screen.findByText(enAgents.pickers.model_source_configured)).toBeTruthy();
    fireEvent.click(await screen.findByText("Configured system model"));
    expect(onChange).toHaveBeenCalledWith("configured-model");
  });
  afterEach(() => { cleanup(); configuration = []; pending = false; });

  it("shows a failed discovery reason instead of an authoritative empty catalog", async () => {
    response = catalog({ status: "timeout", error: "daemon did not respond within 30 seconds" });
    const onChange = renderPicker("saved-model");
    expect(await screen.findByText(enAgents.pickers.model_discovery_failed_title)).toBeTruthy();
    expect(screen.getByText("daemon did not respond within 30 seconds")).toBeTruthy();
    expect(screen.queryByText(enAgents.pickers.model_empty)).toBeNull();
    expect(onChange).not.toHaveBeenCalled();

    fireEvent.change(screen.getByRole("textbox"), { target: { value: "manual-model" } });
    fireEvent.click(screen.getByText(/manual-model/));
    expect(onChange).toHaveBeenCalledWith("manual-model");
  });

  it("keeps empty catalog wording for successful empty discovery", async () => {
    response = catalog();
    renderPicker();
    expect(await screen.findByText(enAgents.pickers.model_empty)).toBeTruthy();
    expect(screen.queryByText(enAgents.pickers.model_discovery_failed_title)).toBeNull();
  });

  it("submits an official opaque model ID unchanged", async () => {
    const id = '["deepseek-official","deepseek-flash"]';
    response = catalog({ models: [{ id, label: "DeepSeek Flash" }] });
    const onChange = renderPicker();
    fireEvent.click(await screen.findByText("DeepSeek Flash"));
    expect(onChange).toHaveBeenCalledWith(id);
  });
});
