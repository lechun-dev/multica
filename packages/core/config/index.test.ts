import { beforeEach, describe, expect, it } from "vitest";
import {
  configStore,
  projectPermissionWritesEnabled,
  type ProjectPermissionRolloutPhase,
} from ".";

describe("project permission rollout config", () => {
  beforeEach(() => {
    configStore.getState().setAuthConfig({ allowSignup: true });
  });

  it.each<ProjectPermissionRolloutPhase>(["off", "shadow", "reader"])(
    "keeps ACL writes disabled in %s",
    (phase) => {
      expect(projectPermissionWritesEnabled(phase)).toBe(false);
    },
  );

  it.each<ProjectPermissionRolloutPhase>(["writer", "restricted"])(
    "enables ACL writes in %s",
    (phase) => {
      expect(projectPermissionWritesEnabled(phase)).toBe(true);
    },
  );

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
