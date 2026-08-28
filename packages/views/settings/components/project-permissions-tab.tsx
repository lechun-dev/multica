"use client";

import { useMemo, useState } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects";
import { useAuthStore } from "@multica/core/auth";
import type { ProjectPermissionRole } from "@multica/core/types";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { toast } from "sonner";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";
import { useT } from "../../i18n";

const NO_ACCESS = "__no_project_access__";
const BUILTIN_ROLES = ["owner", "manager", "member", "viewer"];

// 2026-08-28 coder(lq): Project cells represent explicit project membership;
// workspace-level roles are shown separately and must not be copied here.
export function projectPermissionCellValue(explicitRole?: string | null): string {
  const role = explicitRole?.trim();
  return role || NO_ACCESS;
}

function projectMembersKey(projectId: string) {
  return ["project-members", projectId] as const;
}

// 2026-08-28 coder(lq): Compose this report from existing project/member
// endpoints so the private authorization overlay remains low-conflict with
// future upstream settings-page updates.
export function ProjectPermissionsTab() {
  const { t } = useT("settings");
  const workspaceId = useWorkspaceId();
  const queryClient = useQueryClient();
  const currentUser = useAuthStore((state) => state.user);
  const [savingCell, setSavingCell] = useState<string | null>(null);

  const { data: members = [], isLoading: membersLoading } = useQuery(memberListOptions(workspaceId));
  const { data: projects = [], isLoading: projectsLoading } = useQuery(projectListOptions(workspaceId));
  const { data: roleDefinitions } = useQuery({
    queryKey: ["project-permission-roles", workspaceId],
    queryFn: () => api.listProjectPermissionRoles(),
    enabled: !!workspaceId,
  });
  const projectMemberQueries = useQueries({
    queries: projects.map((project) => ({
      queryKey: projectMembersKey(project.id),
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
  const projectMembersByProject = useMemo(
    () => new Map(projects.map((project, index) => [project.id, projectMemberQueries[index]?.data?.members ?? []])),
    [projectMemberQueries, projects],
  );
  const canManageByProject = useMemo(
    () => new Map(projects.map((project, index) => [project.id, projectMemberQueries[index]?.data?.can_manage ?? false])),
    [projectMemberQueries, projects],
  );
  const loading = membersLoading || projectsLoading || projectMemberQueries.some((query) => query.isLoading);
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

  const saveCell = async (projectId: string, userId: string, value: string) => {
    const cellKey = `${projectId}:${userId}`;
    setSavingCell(cellKey);
    try {
      if (value === NO_ACCESS) await api.removeProjectMember(projectId, userId);
      else await api.addProjectMember(projectId, { user_id: userId, role: value });
      await queryClient.invalidateQueries({ queryKey: projectMembersKey(projectId) });
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
            <div className="overflow-auto">
              <table className="w-full min-w-[64rem] text-body">
                <thead>
                  <tr className="border-b border-surface-border text-left text-caption text-muted-foreground">
                    <th className="sticky left-0 z-20 min-w-56 bg-surface p-2">{t(($) => $.permission_report.person_column)}</th>
                    <th className="sticky left-56 z-20 min-w-32 bg-surface p-2">{t(($) => $.permission_report.workspace_role_column)}</th>
                    {projects.map((project) => <th key={project.id} className="min-w-36 p-2" title={project.title}>{project.title}</th>)}
                  </tr>
                </thead>
                <tbody>
                  {members.map((member) => (
                    <tr key={member.user_id} className="border-b border-surface-border/60">
                      <td className="sticky left-0 z-10 min-w-56 bg-surface p-2">
                        <div>{userLabel(member.user_id)}</div>
                        {member.email && <div className="text-caption text-muted-foreground">{member.email}</div>}
                      </td>
                      <td className="sticky left-56 z-10 min-w-32 bg-surface p-2">{roleLabel(member.role)}</td>
                      {projects.map((project) => {
                        const projectMembers = projectMembersByProject.get(project.id) ?? [];
                        const explicit = projectMembers.find((projectMember) => projectMember.user_id === member.user_id);
                        const value = projectPermissionCellValue(explicit?.role);
                        const canManage = isWorkspaceOwner || canManageByProject.get(project.id) === true;
                        const cellKey = `${project.id}:${member.user_id}`;
                        const disabled = !canManage || savingCell === cellKey;
                        return (
                          <td key={project.id} className="p-2 align-middle">
                            <Select
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
                            </Select>
                          </td>
                        );
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
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
