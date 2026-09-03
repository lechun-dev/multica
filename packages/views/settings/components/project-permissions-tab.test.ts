import { describe, expect, it, vi } from "vitest";

import {
  listAllProjectPermissionReport,
  projectPermissionCellValue,
  projectPermissionGrantOrigin,
  projectPermissionRevokeGrant,
  strongestProjectRole,
} from "./project-permissions-tab";

describe("projectPermissionCellValue", () => {
  it("does not turn a workspace owner into an implicit project owner", () => {
    expect(projectPermissionCellValue(undefined)).toBe("__no_project_access__");
  });

  it("keeps an explicitly assigned project role", () => {
    expect(projectPermissionCellValue(" owner ")).toBe("owner");
  });

  it("treats empty project roles as no explicit project authorization", () => {
    expect(projectPermissionCellValue("  ")).toBe("__no_project_access__");
  });
});

describe("projectPermissionRevokeGrant", () => {
  it("uses the existing direct role when removing a matrix cell", () => {
    expect(projectPermissionRevokeGrant("u1", { directRole: "member" })).toEqual({
      subject_type: "user",
      subject_id: "u1",
      role: "member",
    });
  });

  it("does not revoke inherited-only access", () => {
    expect(projectPermissionRevokeGrant("u1", { directRole: undefined })).toBeUndefined();
    expect(projectPermissionRevokeGrant("u1")).toBeUndefined();
  });
});

describe("strongestProjectRole", () => {
  it("prefers the stronger built-in role regardless of report row order", () => {
    expect(strongestProjectRole("viewer", "manager")).toBe("manager");
    expect(strongestProjectRole("manager", "viewer")).toBe("manager");
  });

  it("uses custom role permission count and a stable key tie-breaker", () => {
    const roles = new Map([
      ["custom-a", { permissions: ["project.view"] }],
      ["custom-b", { permissions: ["project.view", "project.edit"] }],
      ["custom-c", { permissions: ["project.view"] }],
    ] as const);
    expect(strongestProjectRole("custom-a", "custom-b", roles)).toBe("custom-b");
    expect(strongestProjectRole("custom-c", "custom-a", roles)).toBe("custom-c");
  });
});

describe("projectPermissionGrantOrigin", () => {
  const reportRow = {
    scope: "project" as const,
    project_id: "p1",
    project_title: "One",
    user_id: "u1",
    user_name: "A",
    user_email: "a@example.com",
    permission: "project.view" as const,
    source: "manual",
    inherited_from_project: false,
  };

  it("keeps a user grant editable only when it targets that user", () => {
    expect(projectPermissionGrantOrigin({ ...reportRow, subject_type: "user", subject_id: "u1" })).toBe("direct");
  });

  it("preserves expanded department, everyone, and role grant origins", () => {
    expect(projectPermissionGrantOrigin({ ...reportRow, subject_type: "organization", subject_id: "dept-1" })).toBe("organization");
    expect(projectPermissionGrantOrigin({ ...reportRow, subject_type: "everyone", subject_id: "workspace-1" })).toBe("everyone");
    expect(projectPermissionGrantOrigin({ ...reportRow, subject_type: "role", subject_id: "member" })).toBe("role");
  });

  it("does not treat workspace-owner bypass rows as direct grants", () => {
    expect(projectPermissionGrantOrigin({ ...reportRow, subject_type: "user", subject_id: "u1", source: "workspace_role" })).toBe("workspace_role");
  });
});

describe("listAllProjectPermissionReport", () => {
  it("loads every project and task page", async () => {
    const listPage = vi.fn()
      .mockResolvedValueOnce({ rows: [{ scope: "project", project_id: "p1", project_title: "One", user_id: "u1", user_name: "A", user_email: "a@example.com", subject_type: "user", permission: "project.view", source: "manual", inherited_from_project: false }], total: 2 })
      .mockResolvedValueOnce({ rows: [{ scope: "issue", project_id: "p1", project_title: "One", issue_id: "i1", issue_title: "Task", user_id: "u1", user_name: "A", user_email: "a@example.com", subject_type: "user", permission: "project.issue.comment", source: "manual", inherited_from_project: true }], total: 2 });

    const result = await listAllProjectPermissionReport(listPage);

    expect(result.rows).toHaveLength(2);
    expect(listPage).toHaveBeenNthCalledWith(1, { scope: "all", limit: 500, offset: 0 });
    expect(listPage).toHaveBeenNthCalledWith(2, { scope: "all", limit: 500, offset: 1 });
  });
});
