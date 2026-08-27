"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ApiError, api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects";
import { useAuthStore } from "@multica/core/auth";
import type {
  ProjectPermissionReportPermission,
  ProjectPermissionReportRole,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";
import { useT } from "../../i18n";

const ALL = "__all__";
const PAGE_SIZE = 50;
const REPORT_ROLES: ProjectPermissionReportRole[] = ["owner", "admin", "manager", "member", "viewer"];
const REPORT_PERMISSIONS: ProjectPermissionReportPermission[] = [
  "project.view",
  "project.edit",
  "project.issue.create",
  "project.issue.manage",
  "project.agent.use",
  "project.member.manage",
  "project.settings.manage",
];

// 2026-08-27 coder(lq): Keep report controls local to this tab so upstream
// settings changes are less likely to conflict with the project permissions UI.
export function ProjectPermissionsTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const currentUser = useAuthStore((state) => state.user);
  const [projectId, setProjectId] = useState("");
  const [userId, setUserId] = useState("");
  const [role, setRole] = useState<ProjectPermissionReportRole | "">("");
  const [permission, setPermission] = useState<ProjectPermissionReportPermission | "">("");
  const [offset, setOffset] = useState(0);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: projects = [], isLoading: projectsLoading } = useQuery(projectListOptions(wsId));
  const { data: roleDefinitions } = useQuery({
    queryKey: ["project-permission-roles", wsId],
    queryFn: () => api.listProjectPermissionRoles(),
    enabled: !!wsId,
  });
  const reportRoles = useMemo(
    () => Array.from(new Set([
      ...REPORT_ROLES,
      ...(roleDefinitions?.roles ?? []).map((definition) => definition.key),
    ])),
    [roleDefinitions?.roles],
  );
  const isWorkspaceOwner = members.some(
    (member) => member.user_id === currentUser?.id && member.role === "owner",
  );

  // A non-owner must scope the report to one project. Selecting the first
  // visible project keeps the report useful without requesting a forbidden
  // workspace-wide report.
  useEffect(() => {
    if (!isWorkspaceOwner && !projectId && projects[0]) setProjectId(projects[0].id);
  }, [isWorkspaceOwner, projectId, projects]);

  const reportEnabled = isWorkspaceOwner || !!projectId;
  const { data, isLoading, error } = useQuery({
    queryKey: ["project-permission-report", wsId, projectId, userId, role, permission, offset],
    queryFn: () =>
      api.listProjectPermissionReport({
        project_id: projectId || undefined,
        user_id: userId || undefined,
        role: role || undefined,
        permission: permission || undefined,
        scope: "project",
        limit: PAGE_SIZE,
        offset,
      }),
    enabled: reportEnabled,
  });

  const users = useMemo(
    () => members
      .slice()
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((member) => ({ value: member.user_id, label: `${member.name} (${member.email})` })),
    [members],
  );
  const total = data?.total ?? 0;
  const page = Math.floor(offset / PAGE_SIZE) + 1;
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const isForbidden = error instanceof ApiError && error.status === 403;
  const roleLabel = (value: ProjectPermissionReportRole) => {
    const definition = roleDefinitions?.roles.find((item) => item.key === value);
    if (definition?.name) return definition.name;
    if (REPORT_ROLES.includes(value)) {
      return t(($) => $.permission_report.roles[value as "owner" | "admin" | "manager" | "member" | "viewer"]);
    }
    return value;
  };
  const permissionLabel = (value: ProjectPermissionReportPermission) => t(($) => $.permission_report.permissions[value]);
  const resetOffset = () => setOffset(0);

  return (
    <SettingsTab
      title={t(($) => $.permission_report.title)}
      description={t(($) => $.permission_report.description)}
    >
      <SettingsSection>
        <SettingsCard>
          <div className="flex flex-wrap items-end gap-3 border-b border-surface-border pb-4">
            <div className="min-w-52 flex-1 space-y-1">
              <label className="text-caption font-medium" htmlFor="permission-report-project">
                {t(($) => $.permission_report.project_filter)}
              </label>
              <Select
                items={[
                  ...(isWorkspaceOwner ? [{ value: ALL, label: t(($) => $.permission_report.all_projects) }] : []),
                  ...projects.map((project) => ({ value: project.id, label: project.title })),
                ]}
                value={projectId || (isWorkspaceOwner ? ALL : undefined)}
                onValueChange={(value) => {
                  setProjectId(value === ALL ? "" : value ?? "");
                  resetOffset();
                }}
              >
                <SelectTrigger id="permission-report-project" className="w-full">
                  <SelectValue placeholder={t(($) => $.permission_report.select_project)} />
                </SelectTrigger>
                <SelectContent>
                  {isWorkspaceOwner ? <SelectItem value={ALL}>{t(($) => $.permission_report.all_projects)}</SelectItem> : null}
                  {projects.map((project) => <SelectItem key={project.id} value={project.id}>{project.title}</SelectItem>)}
                </SelectContent>
              </Select>
            </div>

            <div className="min-w-52 flex-1 space-y-1">
              <label className="text-caption font-medium" htmlFor="permission-report-user">
                {t(($) => $.permission_report.person_filter)}
              </label>
              <Select
                items={[{ value: ALL, label: t(($) => $.permission_report.all_people) }, ...users]}
                value={userId || ALL}
                onValueChange={(value) => {
                  setUserId(value === ALL ? "" : value ?? "");
                  resetOffset();
                }}
              >
                <SelectTrigger id="permission-report-user" className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t(($) => $.permission_report.all_people)}</SelectItem>
                  {users.map((user) => <SelectItem key={user.value} value={user.value}>{user.label}</SelectItem>)}
                </SelectContent>
              </Select>
            </div>

            <div className="min-w-40 flex-1 space-y-1">
              <label className="text-caption font-medium" htmlFor="permission-report-role">
                {t(($) => $.permission_report.role_filter)}
              </label>
              <Select
                items={[{ value: ALL, label: t(($) => $.permission_report.all_roles) }, ...reportRoles.map((value) => ({ value, label: roleLabel(value) }))]}
                value={role || ALL}
                onValueChange={(value) => {
                  setRole(value === ALL ? "" : (value as ProjectPermissionReportRole) ?? "");
                  resetOffset();
                }}
              >
                <SelectTrigger id="permission-report-role" className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t(($) => $.permission_report.all_roles)}</SelectItem>
                  {reportRoles.map((value) => <SelectItem key={value} value={value}>{roleLabel(value)}</SelectItem>)}
                </SelectContent>
              </Select>
            </div>

            <div className="min-w-56 flex-1 space-y-1">
              <label className="text-caption font-medium" htmlFor="permission-report-permission">
                {t(($) => $.permission_report.permission_filter)}
              </label>
              <Select
                items={[{ value: ALL, label: t(($) => $.permission_report.all_permissions) }, ...REPORT_PERMISSIONS.map((value) => ({ value, label: permissionLabel(value) }))]}
                value={permission || ALL}
                onValueChange={(value) => {
                  setPermission(value === ALL ? "" : (value as ProjectPermissionReportPermission) ?? "");
                  resetOffset();
                }}
              >
                <SelectTrigger id="permission-report-permission" className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t(($) => $.permission_report.all_permissions)}</SelectItem>
                  {REPORT_PERMISSIONS.map((value) => <SelectItem key={value} value={value}>{permissionLabel(value)}</SelectItem>)}
                </SelectContent>
              </Select>
            </div>
          </div>

          {projectsLoading ? (
            <p className="py-6 text-caption text-muted-foreground">{t(($) => $.permission_report.loading)}</p>
          ) : !isWorkspaceOwner && !projectId ? (
            <p className="py-6 text-caption text-muted-foreground">{t(($) => $.permission_report.select_project_hint)}</p>
          ) : isLoading ? (
            <p className="py-6 text-caption text-muted-foreground">{t(($) => $.permission_report.loading)}</p>
          ) : error ? (
            <p className="py-6 text-caption text-destructive">
              {isForbidden ? t(($) => $.permission_report.forbidden) : t(($) => $.permission_report.load_failed)}
            </p>
          ) : data?.rows.length ? (
            <>
              <div className="overflow-auto">
                <table className="w-full min-w-[52rem] text-body">
                  <thead>
                    <tr className="border-b border-surface-border text-left text-caption text-muted-foreground">
                      <th className="p-2">{t(($) => $.permission_report.project_column)}</th>
                      <th className="p-2">{t(($) => $.permission_report.person_column)}</th>
                      <th className="p-2">{t(($) => $.permission_report.workspace_role_column)}</th>
                      <th className="p-2">{t(($) => $.permission_report.project_role_column)}</th>
                      <th className="p-2">{t(($) => $.permission_report.permission_column)}</th>
                      <th className="p-2">{t(($) => $.permission_report.source_column)}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.rows.map((row) => (
                      <tr key={`${row.project_id}:${row.user_id}:${row.permission}:${row.source}`} className="border-b border-surface-border/60">
                        <td className="max-w-56 truncate p-2" title={row.project_title}>{row.project_title}</td>
                        <td className="p-2"><div>{row.user_name}</div><div className="text-caption text-muted-foreground">{row.user_email}</div></td>
                        <td className="p-2">{row.workspace_role ? roleLabel(row.workspace_role) : "—"}</td>
                        <td className="p-2">{row.project_role ? roleLabel(row.project_role) : "—"}</td>
                        <td className="p-2">{permissionLabel(row.permission)}</td>
                        <td className="p-2">{t(($) => $.permission_report.sources[row.source])}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <div className="flex flex-wrap items-center justify-between gap-3 pt-4 text-caption text-muted-foreground">
                <span>{t(($) => $.permission_report.results_summary, { from: offset + 1, to: Math.min(offset + PAGE_SIZE, total), total })}</span>
                <div className="flex items-center gap-2">
                  <Button variant="outline" size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}>{t(($) => $.permission_report.previous)}</Button>
                  <span>{t(($) => $.permission_report.page, { page, pages: pageCount })}</span>
                  <Button variant="outline" size="sm" disabled={page >= pageCount} onClick={() => setOffset(offset + PAGE_SIZE)}>{t(($) => $.permission_report.next)}</Button>
                </div>
              </div>
            </>
          ) : (
            <p className="py-6 text-caption text-muted-foreground">{t(($) => $.permission_report.empty)}</p>
          )}
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}
