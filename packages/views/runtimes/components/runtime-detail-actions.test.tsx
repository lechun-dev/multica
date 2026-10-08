// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { render, screen, within } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";
import { RuntimeDetailPage } from "./runtime-detail-page";

const state = vi.hoisted(() => ({ current: true, placeholder: false }));
vi.mock("@tanstack/react-query", async (original) => ({
  ...await original<typeof import("@tanstack/react-query")>(),
  useQuery: () => ({ data: [], isLoading: false }),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/auth", () => {
  const auth = { user: { id: "user-1" } };
  return { useAuthStore: Object.assign((selector: (value: typeof auth) => unknown) => selector(auth), { getState: () => auth }) };
});
vi.mock("@multica/core/paths", () => ({ useWorkspacePaths: () => ({ runtimes: () => "/runtimes" }) }));
vi.mock("@multica/core/realtime", () => ({ useWSEvent: () => {} }));
vi.mock("@multica/core/agents", () => ({ agentTaskSnapshotOptions: () => ({ queryKey: ["tasks"] }) }));
vi.mock("@multica/core/runtimes", () => ({ runtimeProfileListOptions: () => ({ queryKey: ["profiles"] }) }));
vi.mock("@multica/core/runtimes/queries", () => ({
  runtimeListOptions: () => ({ queryKey: ["runtimes"] }), runtimeKeys: { all: () => ["runtimes"] },
}));
vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({ queryKey: ["agents"] }), memberListOptions: () => ({ queryKey: ["members"] }),
}));
vi.mock("./runtime-machines", () => ({
  sharedCustomName: () => "",
  buildRuntimeMachines: () => [{
    id: "machine-1", mode: "local", title: "Test machine", health: "online", isCurrent: state.current,
    section: "local", daemonId: "daemon-1", runningCount: 0, queuedCount: 0,
    runtimes: state.placeholder ? [] : [{ id: "rt-1", owner_id: "user-1" }],
  }],
}));
vi.mock("./runtime-list", () => ({ buildWorkloadIndex: () => new Map(), RuntimeList: () => null }));
vi.mock("./pending-runtime", () => ({ pendingRuntimesForProfiles: (value: { runtimes: unknown[] }) => value.runtimes }));
vi.mock("./machine-cli-section", () => ({ MachineCliSection: () => null }));
vi.mock("./rename-machine-dialog", () => ({ RenameMachineDialog: () => null }));
vi.mock("./runtime-profiles-dialog", () => ({ RuntimeProfilesDialog: () => null }));
vi.mock("../../navigation", () => ({ AppLink: ({ href, children }: { href: string; children: ReactNode }) => <a href={href}>{children}</a> }));
vi.mock("./shared", () => ({ HealthIcon: () => null, useHealthLabel: () => () => "Online" }));

function show(withSetup = true) {
  return render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, runtimes: enRuntimes } }}>
      <RuntimeDetailPage runtimeId="machine-1"
        localMachineLeadingActions={withSetup ? <button>DSH CLI</button> : undefined}
        localMachineActions={<button>View logs</button>} />
    </I18nProvider>,
  );
}
beforeEach(() => { state.current = true; state.placeholder = false; });

describe("machine detail local setup actions", () => {
  it("places desktop setup before Rename and existing lifecycle actions", () => {
    const { container } = show();
    const header = container.querySelector("header")!;
    const buttons = within(header).getAllByRole("button").map((button) => button.textContent);
    expect(buttons).toEqual(["DSH CLI", "Rename machine", "View logs"]);
  });

  it("never renders local setup or lifecycle controls on a remote machine", () => {
    state.current = false;
    show();
    expect(screen.queryByRole("button", { name: "DSH CLI" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "View logs" })).not.toBeInTheDocument();
  });

  it("still offers local setup when the local daemon has no registered runtimes", () => {
    state.placeholder = true;
    show();
    expect(screen.getByRole("button", { name: "DSH CLI" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Rename machine" })).not.toBeInTheDocument();
  });

  it("does not add desktop setup to consumers that omit the slot", () => {
    show(false);
    expect(screen.queryByRole("button", { name: "DSH CLI" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Rename machine" })).toBeInTheDocument();
  });
});
