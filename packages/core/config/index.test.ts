import { beforeEach, describe, expect, it } from "vitest";
import {
  configStore,
} from ".";

describe("project permission rollout config", () => {
  beforeEach(() => {
    configStore.getState().setAuthConfig({ allowSignup: true });
  });

  it("preserves the old boolean capability as a fully enabled fallback", () => {
    configStore.getState().setAuthConfig({
      allowSignup: true,
      projectPermissionsEnabled: true,
    });

    expect(configStore.getState().projectPermissionRolloutPhase).toBe("restricted");
  });

  it("prefers an explicit rollout phase over the compatibility boolean", () => {
    configStore.getState().setAuthConfig({
      allowSignup: true,
      projectPermissionsEnabled: true,
      projectPermissionRolloutPhase: "reader",
    });

    expect(configStore.getState().projectPermissionRolloutPhase).toBe("reader");
  });
});
