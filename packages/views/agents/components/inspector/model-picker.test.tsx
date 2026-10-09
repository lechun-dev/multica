// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { RuntimeModelListRequest } from "@multica/core/types";
import { afterEach, describe, expect, it, vi } from "vitest";
import enAgents from "../../../locales/en/agents.json";
import enCommon from "../../../locales/en/common.json";
import enIssues from "../../../locales/en/issues.json";
import { ModelPicker } from "./model-picker";

let response: RuntimeModelListRequest;
vi.mock("@multica/core/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("@multica/core/api")>(),
  api: { initiateListModels: async () => response },
}));

function renderPicker(value = "") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onChange = vi.fn();
  render(
    <I18nProvider locale="en" resources={{ en: { agents: enAgents, common: enCommon, issues: enIssues } }}>
      <QueryClientProvider client={client}>
        <ModelPicker runtimeId="dsh-runtime" runtimeOnline value={value} onChange={onChange} />
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
  afterEach(cleanup);

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
