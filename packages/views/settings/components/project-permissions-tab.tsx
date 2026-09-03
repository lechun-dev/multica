"use client";

import { useEffect, useMemo, useState } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useConfigStore } from "@multica/core/config";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects";
import { useAuthStore } from "@multica/core/auth";
import type { ProjectPermissionReportParams, ProjectPermissionReportRow, ProjectPermissionRole } from "@multica/core/types";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { toast } from "sonner";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";
import { useT } from "../../i18n";

const NO_ACCESS = "__no_project_access__";
const INHERITED_ACCESS = "__inherited_project_access__";
const ALL_FILTER = "__all__";
const BUILTIN_ROLES = ["owner", "manager", "member", "viewer"];
const BUILTIN_ROLE_RANK: Record<string, number> = {
  owner: 4,
  manager: 3,
  member: 2,
  viewer: 1,
};
const REPORT_PAGE_SIZE = 500;

export async function listAllProjectPermissionReport(
  listPage: (params: ProjectPermissionReportParams) => Promise<{ rows: ProjectPermissionReportRow[]; total: number }> = (params) => api.listProjectPermissionReport(params),
  scope: ProjectPermissionReportParams["scope"] = "all",
) {
  const rows: ProjectPermissionReportRow[] = [];
  let offset = 0;
  let total = 0;
  do {
    // 2026-09-04 coder(lq): Load both project and task rows so the report's
    // detail section reflects task-level grants and project inheritance.
    const page = await listPage({ scope, limit: REPORT_PAGE_SIZE, offset });
    rows.push(...page.rows);
    total = page.total;
    if (page.rows.length === 0) break;
    offset += page.rows.length;
  } while (offset < total);
  return { rows, total, limit: rows.length, offset: 0 };
}

// 2026-08-28 coder(lq): Project cells represent explicit project membership;
// workspace-level roles are shown separately and must not be copied here.
export function projectPermissionCellValue(explicitRole?: string | null): string {
  const role = explicitRole?.trim();
  return role || NO_ACCESS;
}

// 2026-09-04 coder(lq): Matrix cells may display inherited access, but only
// an explicit direct role can be revoked from this project/user cell.
export function projectPermissionRevokeGrant(
  userId: string,
  access?: { directRole?: string },
) {
  if (!access?.directRole) return undefined;
  return { subject_type: "user" as const, subject_id: userId, role: access.directRole };
}

// 2026-09-03 coder(lq): Keep a user's displayed project role deterministic
// when grants arrive through multiple subjects (for example, a direct grant
// plus an organization grant). Built-in roles have an explicit order; custom
// roles use permission count and then their key as stable tie-breakers.
export function strongestProjectRole(
  current: string | undefined,
  candidate: string | undefined,
  roleByKey?: ReadonlyMap<string, { permissions: readonly unknown[] }>,
): string | undefined {
  if (!candidate) return current;
  if (!current) return candidate;
  const rank = (key: string) => [
    BUILTIN_ROLE_RANK[key] ?? 0,
    roleByKey?.get(key)?.permissions.length ?? 0,
    key,
  ] as const;
  const currentRank = rank(current);
  const candidateRank = rank(candidate);
  for (let index = 0; index < currentRank.length; index += 1) {
    const candidateValue = candidateRank[index];
    const currentValue = currentRank[index];
    if (candidateValue === undefined || currentValue === undefined || candidateValue === currentValue) continue;
    return candidateValue > currentValue ? candidate : current;
  }
  return current;
}

export type ProjectPermissionGrantOrigin = "direct" | "organization" | "everyone" | "role" | "workspace_role";

// 2026-09-04 coder(lq): The report expands department, everyone, and role
// grants into user rows. Preserve that origin so the matrix never presents an
// inherited permission as a removable direct user grant.
export function projectPermissionGrantOrigin(row: ProjectPermissionReportRow): ProjectPermissionGrantOrigin {
  if (row.source === "workspace_role") return "workspace_role";
  if (row.subject_type === "user" && (!row.subject_id || row.subject_id === row.user_id)) return "direct";
  if (row.subject_type === "organization") return "organization";
  if (row.subject_type === "everyone") return "everyone";
  if (row.subject_type === "role") return "role";
  return "direct";
}

function projectMembersKey(workspaceId: string, projectId: string) {
  return ["project-members", workspaceId, projectId] as const;
}

// 2026-08-28 coder(lq): Compose this report from existing project/member
// endpoints so the private authorization overlay remains low-conflict with
// future upstream settings-page updates.
export function ProjectPermissionsTab() {
  const { t } = useT("settings");
  const workspaceId = useWorkspaceId();
  const queryClient = useQueryClient();
  const currentUser = useAuthStore((state) => state.user);
  // 2026-09-04 coder(lq): The canonical grants API is authoritative after
  // migration; retain the legacy endpoint only for older deployments that do
  // not advertise the overlay yet.
  const unifiedApi = useConfigStore((state) => state.projectPermissionsEnabled);
  const [savingCell, setSavingCell] = useState<string | null>(null);
  const [projectFilter, setProjectFilter] = useState(ALL_FILTER);
  const [roleFilter, setRoleFilter] = useState(ALL_FILTER);
  const [personFilter, setPersonFilter] = useState(ALL_FILTER);

  useEffect(() => {
    // 2026-08-28 coder(lq): Filters belong to a workspace; never carry an ID
    // from the previous workspace into the next report.
    setProjectFilter(ALL_FILTER);
    setRoleFilter(ALL_FILTER);
    setPersonFilter(ALL_FILTER);
  }, [workspaceId]);

  const { data: members = [], isLoading: membersLoading } = useQuery(memberListOptions(workspaceId));
  const { data: projects = [], isLoading: projectsLoading } = useQuery(projectListOptions(workspaceId));
  const { data: roleDefinitions } = useQuery({
    queryKey: ["project-permission-roles", workspaceId],
    queryFn: () => api.listProjectPermissionRoles(),
    enabled: !!workspaceId,
  });
  const projectMemberQueries = useQueries({
    queries: projects.map((project) => ({
      queryKey: projectMembersKey(workspaceId, project.id),
      queryFn: () => api.listProjectMembers(project.id),
      enabled: !!workspaceId && !!project.id,
    })),
  });

  const roles = useMemo<ProjectPermissionRole[]>(() => {
    const persisted = roleDefinitions?.roles ?? [];
    const keys = new Set(persisted.map((role) => role.key));
    const missing = BUILTIN_ROLES
      .filter((key) => !keys.has(key))
      .map((key) => ({
        id: `system-${workspaceId}-${key}`,
        workspace_id: workspaceId,
        key,
        name: key.charAt(0).toLocaleUpperCase() + key.slice(1),
        description: "",
        permissions: [],
        is_system: true,
      }));
    return [...persisted, ...missing];
  }, [roleDefinitions?.roles, workspaceId]);
  const roleByKey = useMemo(() => new Map(roles.map((role) => [role.key, role])), [roles]);
  const workspaceMemberByUser = useMemo(
    () => new Map(members.map((member) => [member.user_id, member])),
    [members],
  );
  const isWorkspaceOwner = workspaceMemberByUser.get(currentUser?.id ?? "")?.role === "owner";
  const { data: permissionReport, isLoading: reportLoading } = useQuery({
    queryKey: ["project-permission-report", workspaceId],
    queryFn: () => listAllProjectPermissionReport(),
    // 2026-09-03 coder(lq): Workspace-wide reports are restricted to owners;
    // project admins continue using the project-scoped compatibility reads.
    enabled: !!workspaceId && isWorkspaceOwner,
  });
  const projectMembersByProject = useMemo(
    () => new Map(projects.map((project, index) => [project.id, projectMemberQueries[index]?.data?.members ?? []])),
    [projectMemberQueries, projects],
  );
  // 2026-09-03 coder(lq): The matrix is backed by the canonical report rather
  // than project_members. This makes organization/everyone/task grants
  // visible while preserving the familiar people-by-project layout.
  const reportRows = permissionReport?.rows ?? [];
  const effectiveProjectAccess = useMemo(() => {
    const result = new Map<string, {
      role?: string;
      directRole?: string;
      inheritedRole?: string;
      permissions: Set<string>;
      directPermissions: Set<string>;
      inheritedOrigins: Set<ProjectPermissionGrantOrigin>;
    }>();
    if (permissionReport) {
      for (const row of reportRows) {
        if (row.scope !== "project") continue;
        const key = `${row.project_id}:${row.user_id}`;
        const current = result.get(key) ?? {
          permissions: new Set<string>(),
          directPermissions: new Set<string>(),
          inheritedOrigins: new Set<ProjectPermissionGrantOrigin>(),
        };
        const origin = projectPermissionGrantOrigin(row);
        current.role = strongestProjectRole(current.role, row.project_role, roleByKey);
        if (origin === "direct") {
          current.directRole = strongestProjectRole(current.directRole, row.project_role, roleByKey);
          if (!row.project_role) current.directPermissions.add(row.permission);
        } else {
          current.inheritedRole = strongestProjectRole(current.inheritedRole, row.project_role, roleByKey);
          current.inheritedOrigins.add(origin);
        }
        current.permissions.add(row.permission);
        result.set(key, current);
      }
    } else {
      // 2026-09-03 coder(lq): Keep the matrix usable for project admins when
      // the owner-only report endpoint is unavailable.
      for (const project of projects) {
        for (const member of projectMembersByProject.get(project.id) ?? []) {
          const key = `${project.id}:${member.user_id}`;
          const current = result.get(key) ?? {
            permissions: new Set<string>(),
            directPermissions: new Set<string>(),
            inheritedOrigins: new Set<ProjectPermissionGrantOrigin>(),
          };
          current.role = strongestProjectRole(current.role, member.role, roleByKey);
          current.directRole = strongestProjectRole(current.directRole, member.role, roleByKey);
          result.set(key, current);
        }
      }
    }
    return result;
  }, [permissionReport, projectMembersByProject, projects, reportRows, roleByKey]);
  // 2026-08-28 coder(lq): Keep report filtering local to the already loaded
  // matrix. This makes filter changes immediate and avoids a second report
  // endpoint whose write permissions could drift from the matrix controls.
  const filteredProjects = useMemo(
    () => projectFilter === ALL_FILTER ? projects : projects.filter((project) => project.id === projectFilter),
    [projectFilter, projects],
  );
  const filteredMembers = useMemo(
    () => members.filter((member) => {
      if (personFilter !== ALL_FILTER && member.user_id !== personFilter) return false;
      if (roleFilter === ALL_FILTER) return true;
      return filteredProjects.some((project) => {
        const access = effectiveProjectAccess.get(`${project.id}:${member.user_id}`);
        if (roleFilter === NO_ACCESS) {
          return !access || (!access.role && access.permissions.size === 0);
        }
        return access?.role === roleFilter;
      });
    }),
    [effectiveProjectAccess, filteredProjects, members, personFilter, roleFilter],
  );
  const canManageByProject = useMemo(
    () => new Map(projects.map((project, index) => [project.id, projectMemberQueries[index]?.data?.can_manage ?? false])),
    [projectMemberQueries, projects],
  );
  const loading = membersLoading || projectsLoading || reportLoading || projectMemberQueries.some((query) => query.isLoading);
  // The report is an administrative enhancement. A project manager may not
  // be allowed to query the workspace-wide report, but should still retain
  // the editable project matrix supplied by the compatibility endpoints.
  const hasError = projectMemberQueries.some((query) => query.isError);

  const roleLabel = (key: string) => {
    const persistedName = roleByKey.get(key)?.name;
    if (persistedName) return persistedName;
    if (["owner", "admin", ...BUILTIN_ROLES].includes(key)) {
      return t(($) => $.permission_report.roles[key as "owner" | "admin" | "manager" | "member" | "viewer"]);
    }
    return key;
  };
  const userLabel = (userId: string) => {
    const member = workspaceMemberByUser.get(userId);
    return member?.name || member?.email || userId;
  };
  const originLabel = (origin: ProjectPermissionGrantOrigin) =>
    t(($) => $.permission_report.inherited_sources[origin]);

  const saveCell = async (projectId: string, userId: string, value: string) => {
    const cellKey = `${projectId}:${userId}`;
    setSavingCell(cellKey);
    try {
      if (unifiedApi) {
        if (value === NO_ACCESS) {
          const grant = projectPermissionRevokeGrant(userId, effectiveProjectAccess.get(`${projectId}:${userId}`));
          if (grant) await api.revokeProjectAccessGrant(projectId, grant);
        } else {
          await api.createProjectAccessGrant(projectId, {
            subject_type: "user",
            subject_id: userId,
            role: value,
          });
        }
      } else if (value === NO_ACCESS) {
        await api.removeProjectMember(projectId, userId);
      } else {
        await api.addProjectMember(projectId, { user_id: userId, role: value });
      }
      await queryClient.invalidateQueries({ queryKey: projectMembersKey(workspaceId, projectId) });
      await queryClient.invalidateQueries({ queryKey: ["project-permission-report", workspaceId] });
      toast.success(t(($) => $.permission_report.saved));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.permission_report.save_failed));
    } finally {
      setSavingCell(null);
    }
  };

  return (
    <SettingsTab title={t(($) => $.permission_report.title)} description={t(($) => $.permission_report.description)}>
      <SettingsSection>
        <SettingsCard>
          {loading ? (
            <p className="py-6 text-caption text-muted-foreground">{t(($) => $.permission_report.loading)}</p>
          ) : hasError ? (
            <p className="py-6 text-caption text-destructive">{t(($) => $.permission_report.load_failed)}</p>
          ) : members.length === 0 || projects.length === 0 ? (
            <p className="py-6 text-caption text-muted-foreground">{t(($) => $.permission_report.empty)}</p>
          ) : (
            <>
              <div className="flex flex-wrap items-end gap-3 border-b border-surface-border px-4 py-3">
                <div className="flex min-w-44 flex-1 flex-col gap-1">
                  <span className="text-caption text-muted-foreground">{t(($) => $.permission_report.project_filter)}</span>
                  <Select
                    items={[{ value: ALL_FILTER, label: t(($) => $.permission_report.all_projects) }, ...projects.map((project) => ({ value: project.id, label: project.title }))]}
                    value={projectFilter}
                    onValueChange={(value) => value && setProjectFilter(value)}
                  >
                    <SelectTrigger className="w-full" aria-label={t(($) => $.permission_report.project_filter)}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ALL_FILTER}>{t(($) => $.permission_report.all_projects)}</SelectItem>
                      {projects.map((project) => <SelectItem key={project.id} value={project.id}>{project.title}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </div>
                <div className="flex min-w-44 flex-1 flex-col gap-1">
                  <span className="text-caption text-muted-foreground">{t(($) => $.permission_report.role_filter)}</span>
                  <Select
                    items={[{ value: ALL_FILTER, label: t(($) => $.permission_report.all_roles) }, { value: NO_ACCESS, label: t(($) => $.permission_report.no_access) }, ...roles.map((role) => ({ value: role.key, label: roleLabel(role.key) }))]}
                    value={roleFilter}
                    onValueChange={(value) => value && setRoleFilter(value)}
                  >
                    <SelectTrigger className="w-full" aria-label={t(($) => $.permission_report.role_filter)}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ALL_FILTER}>{t(($) => $.permission_report.all_roles)}</SelectItem>
                      <SelectItem value={NO_ACCESS}>{t(($) => $.permission_report.no_access)}</SelectItem>
                      {roles.map((role) => <SelectItem key={role.key} value={role.key}>{roleLabel(role.key)}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </div>
                <div className="flex min-w-44 flex-1 flex-col gap-1">
                  <span className="text-caption text-muted-foreground">{t(($) => $.permission_report.person_filter)}</span>
                  <Select
                    items={[{ value: ALL_FILTER, label: t(($) => $.permission_report.all_people) }, ...members.map((member) => ({ value: member.user_id, label: userLabel(member.user_id) }))]}
                    value={personFilter}
                    onValueChange={(value) => value && setPersonFilter(value)}
                  >
                    <SelectTrigger className="w-full" aria-label={t(($) => $.permission_report.person_filter)}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ALL_FILTER}>{t(($) => $.permission_report.all_people)}</SelectItem>
                      {members.map((member) => <SelectItem key={member.user_id} value={member.user_id}>{userLabel(member.user_id)}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </div>
              </div>
              {filteredProjects.length === 0 || filteredMembers.length === 0 ? (
                <p className="py-6 text-center text-caption text-muted-foreground">{t(($) => $.permission_report.empty)}</p>
              ) : <div className="max-h-[60vh] overflow-auto">
              {/* 2026-09-01 coder(lq): Keep filters and dialog chrome static while
                  the permission matrix scrolls independently. */}
              <table className="w-full min-w-[64rem] border-separate border-spacing-0 text-body">
                <thead>
                  <tr className="border-b border-surface-border text-left text-caption text-muted-foreground">
                    <th className="sticky left-0 top-0 z-30 min-w-56 border-r border-surface-border bg-surface p-2">{t(($) => $.permission_report.person_column)}</th>
                    <th className="sticky top-0 z-20 min-w-32 bg-surface p-2">{t(($) => $.permission_report.workspace_role_column)}</th>
                    {filteredProjects.map((project) => <th key={project.id} className="sticky top-0 z-20 min-w-36 bg-surface p-2" title={project.title}>{project.title}</th>)}
                  </tr>
                </thead>
                <tbody>
                  {filteredMembers.map((member) => (
                    <tr key={member.user_id} className="border-b border-surface-border/60">
                      <td className="sticky left-0 z-10 min-w-56 border-r border-surface-border bg-surface p-2">
                        <div>{userLabel(member.user_id)}</div>
                        {member.email && <div className="text-caption text-muted-foreground">{member.email}</div>}
                      </td>
                      <td className="min-w-32 p-2">{roleLabel(member.role)}</td>
                      {filteredProjects.map((project) => {
                        const access = effectiveProjectAccess.get(`${project.id}:${member.user_id}`);
                        const value = projectPermissionCellValue(access?.directRole);
                        const directPermissions = !access?.directRole && access?.directPermissions.size
                          ? [...access.directPermissions].join(", ")
                          : "";
                        const inheritedOrigins = [...(access?.inheritedOrigins ?? [])];
                        const inheritedLabel = inheritedOrigins.map(originLabel).join(" / ");
                        const inheritedRoleLabel = access?.inheritedRole ? roleLabel(access.inheritedRole) : "";
                        const canManage = isWorkspaceOwner || canManageByProject.get(project.id) === true;
                        const cellKey = `${project.id}:${member.user_id}`;
                        const disabled = !canManage || savingCell === cellKey || !!directPermissions;
                        return (
                          <td key={project.id} className="p-2 align-middle">
                            {directPermissions ? (
                              <span className="inline-flex max-w-44 truncate rounded border px-2 py-1 text-caption" title={directPermissions}>{directPermissions}</span>
                            ) : access?.directRole || !access?.inheritedRole ? <Select
                                items={[{ value: NO_ACCESS, label: t(($) => $.permission_report.no_access) }, ...roles.map((role) => ({ value: role.key, label: roleLabel(role.key) }))]}
                                value={value}
                                onValueChange={(next) => next && void saveCell(project.id, member.user_id, next)}
                                disabled={disabled}
                              >
                                <SelectTrigger className="w-32" aria-label={`${userLabel(member.user_id)} / ${project.title}`}>
                                  <SelectValue />
                                </SelectTrigger>
                                <SelectContent>
                                  <SelectItem value={NO_ACCESS}>{t(($) => $.permission_report.no_access)}</SelectItem>
                                  {roles.map((role) => <SelectItem key={role.key} value={role.key}>{roleLabel(role.key)}</SelectItem>)}
                                </SelectContent>
                              </Select> : <Select
                                items={[{ value: INHERITED_ACCESS, label: inheritedRoleLabel }, ...roles.map((role) => ({ value: role.key, label: roleLabel(role.key) }))]}
                                value={INHERITED_ACCESS}
                                onValueChange={(next) => next && next !== INHERITED_ACCESS && void saveCell(project.id, member.user_id, next)}
                                disabled={!canManage || savingCell === cellKey}
                              >
                                <SelectTrigger className="w-32" aria-label={`${userLabel(member.user_id)} / ${project.title}`}>
                                  <SelectValue />
                                </SelectTrigger>
                                <SelectContent>
                                  <SelectItem value={INHERITED_ACCESS} disabled>{inheritedRoleLabel}</SelectItem>
                                  {roles.map((role) => <SelectItem key={role.key} value={role.key}>{roleLabel(role.key)}</SelectItem>)}
                                </SelectContent>
                              </Select>}
                            {inheritedLabel && (
                              <div className="mt-1 max-w-44 truncate text-caption text-muted-foreground" title={`${t(($) => $.permission_report.inherited_grant)}: ${inheritedLabel}`}>
                                {t(($) => $.permission_report.inherited_grant)}: {inheritedLabel}
                              </div>
                            )}
                          </td>
                        );
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
              {reportRows.length > 0 && <div className="border-t border-surface-border p-3">
                <div className="mb-2 text-caption font-medium text-muted-foreground">{t(($) => $.permission_report.effective_permissions)}</div>
                <div className="max-h-56 overflow-auto text-caption">
                  {reportRows.slice(0, 200).map((row: ProjectPermissionReportRow, index) => (
                    <div key={`${row.scope}:${row.project_id}:${row.issue_id ?? ""}:${row.user_id}:${row.permission}:${index}`} className="flex gap-3 border-b border-surface-border/50 py-1">
                      <span className="w-20 shrink-0">{row.scope}</span>
                      <span className="min-w-32 truncate">{row.project_title}{row.issue_title ? ` / ${row.issue_title}` : ""}</span>
                      <span className="min-w-28 truncate">{userLabel(row.user_id)}</span>
                      <span className="min-w-36 truncate">{row.permission}</span>
                      <span className="min-w-28 truncate text-muted-foreground">{row.source}{row.inherited_from_project ? " · inherited" : ""}</span>
                    </div>
                  ))}
                </div>
              </div>}
            </div>}
            </>
          )}
          <div className="mt-3 flex items-center gap-2 text-caption text-muted-foreground">
            <span>{t(($) => $.permission_report.people_projects)}</span>
            <span>·</span>
            <span>{t(($) => $.permission_report.read_only_hint)}</span>
          </div>
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}
