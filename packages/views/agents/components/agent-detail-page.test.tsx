// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError } from "@multica/core/api";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../navigation";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

vi.mock("./agent-overview-pane", () => ({
  AgentOverviewPane: ({
    agent, canEdit, onUpdate,
  }: {
    agent: Agent;
    canEdit: boolean;
    onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
  }) => (
    <section aria-label="Agent settings">
      <output aria-label="Current model">{agent.model}</output>
      <button type="button" disabled={!canEdit} onClick={() => void onUpdate(agent.id, { model: "new-model" })}>
        Save model
      </button>
      <button type="button" onClick={() => void onUpdate(agent.id, { model: "new-model" })}>
        Attempt write
      </button>
    </section>
  ),
}));
vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => <div>actor-avatar</div>,
}));
vi.mock("./agent-presence-indicator", () => ({
  AgentPresenceIndicator: () => null,
}));

const membersRef = vi.hoisted(() => ({ current: [] as unknown[] }));
// When set, the member query never resolves — the "membership still loading"
// window in which the DM decision is undetermined.
const membersPendingRef = vi.hoisted(() => ({ current: false }));
const currentUserRef = vi.hoisted(() => ({
  current: { id: "user-1" } as { id: string } | null,
}));
const mockToastError = vi.hoisted(() => vi.fn());
const mockModalOpen = vi.hoisted(() => vi.fn());
const mockGetAgent = vi.hoisted(() => vi.fn());
const mockOwnerDetail = vi.hoisted(() => vi.fn());
const mockOwnerCatalog = vi.hoisted(() => vi.fn());
const mockUpdateAgent = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/agents", () => ({
  isAgentRuntimeBound: (agent: { runtime_id: string; runtime_bound?: boolean }) =>
    agent.runtime_bound !== false && agent.runtime_id.length > 0,
  useWorkspacePresenceMap: () => ({ byAgent: new Map() }),
}));
vi.mock("@multica/core/workspace/queries", async (importOriginal) => ({
  ...await importOriginal<typeof import("@multica/core/workspace/queries")>(),
  memberListOptions: (wsId: string) => ({
    queryKey: ["members", wsId],
    queryFn: () =>
      membersPendingRef.current
        ? new Promise(() => {})
        : Promise.resolve(membersRef.current),
  }),
}));
vi.mock("@multica/core/runtimes", () => ({
  runtimeListOptions: (wsId: string) => ({
    queryKey: ["runtimes", wsId],
    queryFn: () => Promise.resolve([]),
  }),
}));
vi.mock("@multica/core/auth", () => {
  type AuthState = { user: { id: string } | null };
  const state = (): AuthState => ({ user: currentUserRef.current });
  const useAuthStore = Object.assign(
    (selector?: (s: AuthState) => unknown) =>
      selector ? selector(state()) : state(),
    { getState: state },
  );
  return { useAuthStore };
});
vi.mock("@multica/core/modals", () => ({
  useModalStore: Object.assign(vi.fn(), {
    getState: () => ({ open: mockModalOpen }),
  }),
}));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    agents: () => "/acme/agents",
    chat: () => "/acme/chat",
  }),
}));
vi.mock("@multica/core/api", () => {
  class ApiError extends Error {
    status: number;
    constructor(message: string, status: number) {
      super(message);
      this.status = status;
    }
  }
  return {
    api: { getAgent: mockGetAgent, getOwnerAgentDetail: mockOwnerDetail, listOwnerAgentCatalog: mockOwnerCatalog, updateAgent: mockUpdateAgent },
    ApiError,
  };
});
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: mockToastError },
}));

import { AgentDetailPage } from "./agent-detail-page";
import { agentProfileOptions, workspaceKeys } from "@multica/core/workspace/queries";

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Lambda",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-2",
  skills: [],
  created_at: "2026-05-28T00:00:00Z",
  updated_at: "2026-05-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

const ownAgent = { ...baseAgent, owner_id: "user-1" };
const privateAgent = {
  ...baseAgent,
  visibility: "private",
  permission_mode: "private",
  invocation_targets: [],
  model: "private-model",
} satisfies Agent;
const forbidden = () => new ApiError("forbidden", 403, "Forbidden");
const notFound = () => new ApiError("not found", 404, "Not Found");
const profileKey = agentProfileOptions("ws-1", "agent-1", "user-1").queryKey;

function renderPage(seed?: (client: QueryClient) => void) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  seed?.(queryClient);
  const push = vi.fn();
  const navigation: NavigationAdapter = {
    push,
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => path,
  };
  const page = () => (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <AgentDetailPage agentId="agent-1" />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>
  );
  const view = render(page());
  return { push, queryClient, ...view, refreshViewer: () => view.rerender(page()) };
}

beforeEach(() => {
  vi.resetAllMocks();
  currentUserRef.current = { id: "user-1" };
  membersRef.current = [{ user_id: "user-1", role: "member" }];
  membersPendingRef.current = false;
  mockGetAgent.mockRejectedValue(forbidden());
  mockOwnerDetail.mockRejectedValue(forbidden());
  mockOwnerCatalog.mockResolvedValue([]);
  mockUpdateAgent.mockResolvedValue({ ...ownAgent, model: "new-model" });
});

describe("AgentDetailPage profile access", () => {
  it.each(["member", "owner", "admin"])("lets a %s edit their own agent", async (role) => {
    membersRef.current = [{ user_id: "user-1", role }];
    mockGetAgent.mockResolvedValue(ownAgent);
    renderPage();

    const save = await screen.findByRole("button", { name: "Save model" });
    expect(save).toBeEnabled();
    fireEvent.click(save);
    expect(mockUpdateAgent).toHaveBeenCalledWith("agent-1", { model: "new-model" });
    expect(mockOwnerDetail).not.toHaveBeenCalled();
  });

  it("uses the same settings pane for a workspace Owner with controls disabled", async () => {
    membersRef.current = [{ user_id: "user-1", role: "owner" }];
    mockOwnerDetail.mockResolvedValue({
      id: privateAgent.id, owner_id: privateAgent.owner_id, profile: privateAgent,
    });
    const { push, queryClient } = renderPage();

    expect(await screen.findByRole("heading", { name: privateAgent.name })).toBeInTheDocument();
    expect(screen.getByLabelText("Current model")).toHaveTextContent("private-model");
    expect(screen.getByRole("button", { name: "Save model" })).toBeDisabled();
    const dm = screen.getByRole("button", { name: "DM" });
    expect(dm).toBeDisabled();
    expect(dm).not.toHaveAttribute("href");
    expect(screen.getByRole("button", { name: "Assign work" })).toBeDisabled();
    expect(screen.getByLabelText("Agent actions")).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Attempt write" }));
    expect(mockUpdateAgent).not.toHaveBeenCalled();
    expect(mockModalOpen).not.toHaveBeenCalled();
    expect(push).not.toHaveBeenCalled();
    expect(queryClient.getQueryData(workspaceKeys.agent("ws-1", "agent-1"))).toBeUndefined();
    expect(queryClient.getQueryData(workspaceKeys.agents("ws-1"))).toBeUndefined();
  });

  it.each(["member", "admin"])("denies a %s with a shared invocation grant", async (role) => {
    membersRef.current = [{ user_id: "user-1", role }];
    mockGetAgent.mockResolvedValue(baseAgent);
    renderPage((client) => client.setQueryData(workspaceKeys.agents("ws-1"), [baseAgent]));

    expect(await screen.findByText(enAgents.detail.no_access_title)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Agent settings" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: baseAgent.name })).not.toBeInTheDocument();
  });

  it("does not show cached detail before the current link is authorized", async () => {
    mockGetAgent.mockImplementation(() => new Promise(() => {}));
    const { container } = renderPage((client) => client.setQueryData(profileKey, privateAgent));

    await waitFor(() => expect(mockGetAgent).toHaveBeenCalledWith("agent-1"));
    expect(screen.queryByRole("heading", { name: privateAgent.name })).not.toBeInTheDocument();
    expect(container.querySelector('[data-slot="skeleton"]')).not.toBeNull();
  });

  it("drops the previous viewer's profile immediately when switching accounts", async () => {
    mockGetAgent.mockResolvedValueOnce(ownAgent);
    const { refreshViewer } = renderPage();
    await screen.findByRole("heading", { name: ownAgent.name });

    currentUserRef.current = { id: "user-3" };
    refreshViewer();

    expect(screen.queryByRole("heading", { name: ownAgent.name })).not.toBeInTheDocument();
    expect(await screen.findByText(enAgents.detail.no_access_title)).toBeInTheDocument();
  });

  it("hides the Owner profile when permission is revoked on refetch", async () => {
    mockOwnerDetail.mockResolvedValueOnce({
      id: privateAgent.id, owner_id: privateAgent.owner_id, profile: privateAgent,
    });
    const { queryClient } = renderPage();
    await screen.findByRole("heading", { name: privateAgent.name });

    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: profileKey });
    });

    expect(await screen.findByText(enAgents.detail.no_access_title)).toBeInTheDocument();
    expect(screen.queryByLabelText("Current model")).not.toBeInTheDocument();
  });

  it("shows not found after the API returns 404", async () => {
    mockGetAgent.mockRejectedValue(notFound());
    renderPage();
    expect(await screen.findByText(enAgents.detail.not_found_title)).toBeInTheDocument();
  });

  it("optimistically updates the viewer's authorized profile", async () => {
    mockGetAgent.mockResolvedValue(ownAgent);
    mockUpdateAgent.mockImplementation(() => new Promise(() => {}));
    const { queryClient } = renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Save model" }));

    expect(await screen.findByText("new-model", { selector: "output" })).toBeInTheDocument();
    expect(queryClient.getQueryData<Agent>(profileKey)).toMatchObject({ model: "new-model" });
  });
});

describe("AgentDetailPage own-agent actions", () => {
  beforeEach(() => {
    mockGetAgent.mockResolvedValue(ownAgent);
  });

  it("opens chat and lets the agent owner assign work", async () => {
    const { push } = renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "DM" }));
    expect(push).toHaveBeenCalledWith("/acme/chat?agent=agent-1");
    fireEvent.click(screen.getByRole("button", { name: "Assign work" }));
    expect(mockModalOpen).toHaveBeenCalledWith("quick-create-issue", { agent_id: ownAgent.id });
  });

  it("disables DM while membership is resolving", async () => {
    membersPendingRef.current = true;
    const { push } = renderPage();
    const dm = await screen.findByRole("button", { name: "DM" });
    expect(dm).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(dm);
    expect(push).not.toHaveBeenCalled();
  });

  it("shows the restore action on the archived profile", async () => {
    mockGetAgent.mockResolvedValue({ ...ownAgent, archived_at: "2026-06-01T00:00:00Z" });
    renderPage();
    expect(await screen.findByRole("button", { name: "Restore" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "DM" })).not.toBeInTheDocument();
  });

  it("explains a missing runtime to the agent owner", async () => {
    mockGetAgent.mockResolvedValue({ ...ownAgent, runtime_id: "", runtime_bound: false });
    renderPage();
    expect(await screen.findByRole("button", { name: "Bind runtime" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "DM" }));
    expect(mockToastError).toHaveBeenCalledWith("Bind a runtime before running this agent.");
  });
});
