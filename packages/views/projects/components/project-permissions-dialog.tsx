"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ShieldCheck, UserMinus } from "lucide-react";
import { api } from "@multica/core/api";
import { useConfigStore } from "@multica/core/config";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { toast } from "sonner";
import { useT } from "../../i18n";
import type { ProjectAccessGrant, ProjectAccessGrantSubjectType } from "@multica/core/types";
import { ProjectPermissionOrganizationSelect } from "./project-permission-organization-select";
import { ProjectMemberMultiSelect } from "./project-member-multi-select";

type ProjectRole = string;

// 2026-08-28 coder(lq): Support controlled opens so list actions can reuse this
// dialog without mounting a second authorization implementation.
type ProjectPermissionsDialogProps = {
  projectId: string;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  hideTrigger?: boolean;
};
const BUILTIN_PROJECT_ROLES = ["owner", "manager", "member", "viewer"];
function projectMembersKey(workspaceId: string, projectId: string) {
  return ["project-members", workspaceId, projectId] as const;
}

function formatGrantTime(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? value
    : new Intl.DateTimeFormat(undefined, { dateStyle: "short", timeStyle: "short" }).format(date);
}

// 2026-08-27 coder(lq): Keep the private authorization experience in one
// component so the shared upstream issues header only owns one additive hook.
export function ProjectPermissionsDialog({
  projectId,
  open: controlledOpen,
  onOpenChange,
  hideTrigger = false,
}: ProjectPermissionsDialogProps) {
  const { t } = useT("projects");
  const workspaceId = useWorkspaceId();
  // 2026-09-01 coder(lq): Keep the compatibility switch sourced from the
  // server-advertised capability so older deployments continue using their
  // legacy membership endpoint without an untyped runtime probe.
  const unifiedApi = useConfigStore((state) => state.projectPermissionsEnabled);
  const queryClient = useQueryClient();
  const [internalOpen, setInternalOpen] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [role, setRole] = useState<ProjectRole>("member");
  // 2026-09-04 coder(lq): Project grants are assigned to people, departments,
  // or everyone. Keep role-subject records readable below for compatibility,
  // but do not offer that advanced grant type in the creation UI.
  const [subjectType, setSubjectType] = useState<Exclude<ProjectAccessGrantSubjectType, "role">>("user");
  const [subjectIds, setSubjectIds] = useState<string[]>([]);
  const [granting, setGranting] = useState(false);
  const [removingId, setRemovingId] = useState<string | null>(null);

  const open = controlledOpen ?? internalOpen;
  const setOpen = (nextOpen: boolean) => {
    onOpenChange?.(nextOpen);
    if (controlledOpen === undefined) setInternalOpen(nextOpen);
  };

  const projectMembersQuery = useQuery({
    queryKey: projectMembersKey(workspaceId, projectId),
    queryFn: () => api.listProjectMembers(projectId),
    enabled: !!projectId,
  });
  const accessGrantsQuery = useQuery({
    queryKey: ["project-access-grants", workspaceId, projectId],
    queryFn: () => api.listProjectAccessGrants(projectId),
    // 2026-09-03 coder(lq): Keep legacy deployments on the legacy endpoint;
    // the unified grants route may not exist until the migration is enabled.
    enabled: unifiedApi && !!projectId,
  });
  const directoryQuery = useQuery({
    queryKey: ["project-permission-organizations", workspaceId],
    queryFn: () => api.listProjectAuthorizationOrganizations(workspaceId),
    enabled: open && unifiedApi && !!workspaceId,
    staleTime: 60_000,
  });
  const canManage = projectMembersQuery.data?.can_manage ?? false;
  const { data: workspaceMembers = [], isLoading: membersLoading, isError: membersError } = useQuery({
    ...memberListOptions(workspaceId),
    enabled: open && canManage && !!workspaceId,
  });
  const rolesQuery = useQuery({
    queryKey: ["project-permission-roles", workspaceId],
    queryFn: () => api.listProjectPermissionRoles(),
    enabled: open && canManage && !!workspaceId,
  });
  const roles = useMemo(
    () => {
      const persisted = rolesQuery.data?.roles ?? [];
      const persistedKeys = new Set(persisted.map((role) => role.key));
      // 2026-08-28 coder(lq): Keep built-in roles selectable if an older
      // deployment has not seeded its role catalog yet. Persisted definitions
      // remain first-class and keep their customized names and permissions.
      const missingBuiltIns = BUILTIN_PROJECT_ROLES
        .filter((key) => !persistedKeys.has(key))
        .map((key) => ({
          id: `system-${workspaceId}-${key}`,
          workspace_id: workspaceId,
          key,
          name: key.charAt(0).toLocaleUpperCase() + key.slice(1),
          description: "",
          permissions: [],
          is_system: true,
        }));
      return [...persisted, ...missingBuiltIns];
    },
    [rolesQuery.data?.roles, workspaceId],
  );
  const roleByKey = useMemo(() => new Map(roles.map((item) => [item.key, item])), [roles]);

  const projectMembers = useMemo(
    () => projectMembersQuery.data?.members ?? [],
    [projectMembersQuery.data?.members],
  );
  const roleByUser = useMemo(
    () => new Map(projectMembers.map((member) => [member.user_id, member.role])),
    [projectMembers],
  );
  const unifiedUserGrantIds = useMemo(
    () => new Set(
      (accessGrantsQuery.data?.grants ?? [])
        .filter((grant) => grant.subject_type === "user" && !!grant.subject_id)
        .map((grant) => grant.subject_id as string),
    ),
    [accessGrantsQuery.data?.grants],
  );
  const workspaceMemberByUser = useMemo(
    () => new Map(workspaceMembers.map((member) => [member.user_id, member])),
    [workspaceMembers],
  );
  const organizationById = useMemo(
    () => new Map((directoryQuery.data?.organizations ?? []).map((organization) => [organization.id, organization])),
    [directoryQuery.data?.organizations],
  );
  const currentMembers = useMemo(
    () => projectMembers.map((member) => ({ ...member, profile: workspaceMemberByUser.get(member.user_id) })),
    [projectMembers, workspaceMemberByUser],
  );
  const addableMembers = useMemo(
    () => unifiedApi
      ? workspaceMembers.filter((member) => !unifiedUserGrantIds.has(member.user_id))
      : workspaceMembers.filter((member) => !roleByUser.has(member.user_id)),
    [roleByUser, unifiedApi, unifiedUserGrantIds, workspaceMembers],
  );
  // 2026-09-01 coder(lq): The selected subject depends on the grant type;
  // organization and everyone grants must not be blocked by the user
  // checklist left over from the previous subject type.
  const canGrant = subjectType === "user"
    ? selectedIds.size > 0
    : subjectType === "everyone"
      ? true
      : subjectIds.length > 0;

  useEffect(() => {
    if (open) return;
    setSelectedIds(new Set());
    setRole("member");
    setSubjectType("user");
    setSubjectIds([]);
  }, [open]);

  useEffect(() => {
    // 2026-09-01 coder(lq): Clear stale selections when switching subject
    // types so a grant cannot accidentally submit an unrelated user or ID.
    setSelectedIds(new Set());
    setSubjectIds([]);
  }, [subjectType]);

  const refresh = () => queryClient.invalidateQueries({ queryKey: projectMembersKey(workspaceId, projectId) });
  const refreshGrants = () => queryClient.invalidateQueries({ queryKey: ["project-access-grants", workspaceId, projectId] });
  const toggleMember = (userId: string) => {
    setSelectedIds((current) => {
      const next = new Set(current);
      if (next.has(userId)) next.delete(userId);
      else next.add(userId);
      return next;
    });
  };
  const grantSelected = async () => {
    if (unifiedApi) {
      const selectedSubjectIds = subjectType === "user" ? [...selectedIds] : subjectType === "everyone" ? [""] : subjectIds;
      if ((subjectType === "user" && selectedSubjectIds.length === 0) || (subjectType !== "everyone" && !selectedSubjectIds[0])) return;
      setGranting(true);
      try {
        await Promise.all(selectedSubjectIds.map((id) => api.createProjectAccessGrant(projectId, {
          subject_type: subjectType,
          subject_id: id || undefined,
          role,
        })));
        await refreshGrants();
        setSelectedIds(new Set());
        setSubjectIds([]);
        toast.success(t(($) => $.permissions.grant_success));
      } catch (error) {
        toast.error(error instanceof Error ? error.message : t(($) => $.permissions.grant_failed));
      } finally {
        setGranting(false);
      }
      return;
    }
    if (selectedIds.size === 0) return;
    setGranting(true);
    const userIds = [...selectedIds];
    const results = await Promise.allSettled(
      userIds.map((userId) => api.addProjectMember(projectId, { user_id: userId, role })),
    );
    const failedIds = userIds.filter((_, index) => results[index]?.status === "rejected");
    await refresh();
    setSelectedIds(new Set(failedIds));
    setGranting(false);
    if (failedIds.length === 0) toast.success(t(($) => $.permissions.grant_success));
    else toast.error(t(($) => $.permissions.grant_failed));
  };

  const removeMember = async (userId: string) => {
    setRemovingId(userId);
    try {
      if (unifiedApi) {
        // 2026-08-31 coder(lq): Never mutate the legacy membership table when
        // the unified authorization API is available; this keeps organization,
        // role, and everyone grants in one source of truth.
        const grants = (accessGrantsQuery.data?.grants ?? []).filter(
          (grant) => grant.subject_type === "user" && grant.subject_id === userId,
        );
        await Promise.all(grants.map((grant) => api.revokeProjectAccessGrant(projectId, {
          subject_type: "user",
          subject_id: userId,
          role: grant.role,
          permission: grant.permission,
        })));
        await refreshGrants();
      } else {
        await api.removeProjectMember(projectId, userId);
        await refresh();
      }
      setSelectedIds((current) => {
        const next = new Set(current);
        next.delete(userId);
        return next;
      });
      toast.success(t(($) => $.permissions.remove_success));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.permissions.remove_failed));
    } finally {
      setRemovingId(null);
    }
  };

  const updateGrantRole = async (grant: ProjectAccessGrant, newRole: ProjectRole) => {
    if (!unifiedApi || grant.role === newRole) return;
    try {
      // 2026-09-03 coder(lq): Create the replacement before revoking the old
      // role so a transient request failure cannot accidentally remove access.
      await api.createProjectAccessGrant(projectId, {
        subject_type: grant.subject_type,
        subject_id: grant.subject_id,
        role: newRole,
      });
      await api.revokeProjectAccessGrant(projectId, {
        subject_type: grant.subject_type,
        subject_id: grant.subject_id,
        role: grant.role,
        permission: grant.permission,
      });
      await refreshGrants();
      toast.success(t(($) => $.permissions.update_success));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.permissions.update_failed));
    }
  };

  const removeGrant = async (grant: { subject_type: ProjectAccessGrantSubjectType; subject_id?: string; role?: string; permission?: string }) => {
    try {
      await api.revokeProjectAccessGrant(projectId, {
        subject_type: grant.subject_type,
        subject_id: grant.subject_id,
        role: grant.role,
        permission: grant.permission,
      });
      await refreshGrants();
      toast.success(t(($) => $.permissions.remove_success));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.permissions.remove_failed));
    }
  };

  const updateMemberRole = async (userId: string, newRole: ProjectRole) => {
    try {
      await api.addProjectMember(projectId, { user_id: userId, role: newRole });
      await refresh();
      toast.success(t(($) => $.permissions.update_success));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.permissions.update_failed));
    }
  };

  if (!canManage && !accessGrantsQuery.data) return null;

  const roleItems = roles.map((item) => ({
    value: item.key,
    label: item.name || item.key,
  }));
  return (
    <>
      {!hideTrigger && (
        <Button variant="outline" size="sm" className="shrink-0 gap-1.5" onClick={() => setOpen(true)}>
          <ShieldCheck className="size-3.5" />
          {t(($) => $.permissions.authorize)}
        </Button>
      )}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-4xl">
          <DialogHeader>
            <DialogTitle>{t(($) => $.permissions.dialog_title)}</DialogTitle>
            <DialogDescription>{t(($) => $.permissions.dialog_description)}</DialogDescription>
          </DialogHeader>

          <section role="region" aria-label={t(($) => $.permissions.current_access)} className="space-y-2">
            <div className="flex items-center justify-between">
              <h3 className="text-body font-medium">{t(($) => $.permissions.current_access)}</h3>
              <span className="text-caption text-muted-foreground">{unifiedApi ? (accessGrantsQuery.data?.grants.length ?? 0) : projectMembers.length}</span>
            </div>
            {unifiedApi && accessGrantsQuery.isLoading ? (
              <div className="py-6 text-center text-body text-muted-foreground">{t(($) => $.permissions.loading)}</div>
            ) : unifiedApi && accessGrantsQuery.data ? (
              <div className="max-h-64 overflow-auto rounded-lg border">
                <table className="w-full min-w-[620px] text-body">
                  <thead className="bg-muted/40 text-left text-caption text-muted-foreground">
                    <tr>
                      <th className="px-3 py-2 font-medium">{t(($) => $.permissions.user)}</th>
                      <th className="px-3 py-2 font-medium">{t(($) => $.permissions.project_permission_source)}</th>
                      <th className="px-3 py-2 font-medium">{t(($) => $.permissions.project_permission_role)}</th>
                      <th className="px-3 py-2 font-medium">{t(($) => $.permissions.project_permission_granted_at)}</th>
                      <th className="w-12 px-3 py-2" />
                    </tr>
                  </thead>
                  <tbody>
                    {accessGrantsQuery.data.grants.length === 0 ? (
                      <tr><td colSpan={5} className="px-3 py-6 text-center text-caption text-muted-foreground">{t(($) => $.permissions.current_access_empty)}</td></tr>
                    ) : accessGrantsQuery.data.grants.map((grant) => {
                      const user = grant.subject_id ? workspaceMemberByUser.get(grant.subject_id) : undefined;
                      const organization = grant.subject_id ? organizationById.get(grant.subject_id) : undefined;
                      const roleSubject = grant.subject_id ? roleByKey.get(grant.subject_id) : undefined;
                      const label = grant.subject_type === "everyone"
                        ? t(($) => $.permissions.everyone)
                        : grant.subject_type === "organization"
                          ? t(($) => $.permissions.organization_prefix, { name: organization?.name || grant.subject_id || "" })
                          : grant.subject_type === "role"
                            ? t(($) => $.permissions.role_prefix, { name: roleSubject?.name || grant.subject_id || "" })
                            : user?.name || user?.email || grant.subject_id || "—";
                      const subjectTypeLabel = grant.subject_type === "organization"
                        ? t(($) => $.permissions.organization)
                        : grant.subject_type === "everyone"
                          ? t(($) => $.permissions.everyone)
                          : grant.subject_type === "role"
                            ? t(($) => $.permissions.role)
                            : t(($) => $.permissions.user);
                      const source = grant.source === "manual" ? t(($) => $.permissions.direct_project_grant) : grant.source;
                      // 2026-09-05 coder(lq): The project creator's Owner is a
                      // hard permission. Keep system/migration rows visible in
                      // the audit list, but do not offer mutations that the
                      // server must reject.
                      const isImmutableCreatorOwner = grant.issue_id == null
                        && grant.subject_type === "user"
                        && grant.role === "owner"
                        && (grant.source === "system" || grant.source === "migration");
                      return (
                        <tr key={grant.id || `${grant.subject_type}-${grant.subject_id}-${grant.role}-${grant.permission}`} className="border-t">
                          <td className="px-3 py-2"><div className="font-medium">{label}</div><div className="text-caption text-muted-foreground">{subjectTypeLabel}</div></td>
                          <td className="px-3 py-2 text-muted-foreground">{source}</td>
                          <td className="px-3 py-2">
                            {canManage && !isImmutableCreatorOwner && grant.subject_type !== "role" && grant.role ? (
                              <Select modal={false} items={roleItems} value={grant.role} onValueChange={(value) => value && void updateGrantRole(grant, value)}>
                                <SelectTrigger className="w-32" aria-label={`${t(($) => $.permissions.change_role_aria)} ${label}`}><SelectValue /></SelectTrigger>
                                <SelectContent alignItemWithTrigger={false}>{roles.map((item) => <SelectItem key={item.key} value={item.key}>{item.name || item.key}</SelectItem>)}</SelectContent>
                              </Select>
                            ) : <span>{roleByKey.get(grant.role || "")?.name || grant.role || grant.permission || "—"}</span>}
                          </td>
                          <td className="px-3 py-2 text-muted-foreground">{formatGrantTime(grant.created_at)}</td>
                          <td className="px-3 py-2">{canManage && !isImmutableCreatorOwner && <Button variant="ghost" size="icon-sm" aria-label={`${t(($) => $.permissions.remove_aria)} ${label}`} onClick={() => void removeGrant(grant)}><UserMinus className="size-3.5" /></Button>}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            ) : projectMembersQuery.isLoading ? (
              <div className="py-6 text-center text-body text-muted-foreground">{t(($) => $.permissions.loading)}</div>
            ) : (
              <div className="max-h-64 overflow-auto rounded-lg border">
                <table className="w-full min-w-[620px] text-body">
                  <thead className="bg-muted/40 text-left text-caption text-muted-foreground">
                    <tr>
                      <th className="px-3 py-2 font-medium">{t(($) => $.permissions.user)}</th>
                      <th className="px-3 py-2 font-medium">{t(($) => $.permissions.project_permission_source)}</th>
                      <th className="px-3 py-2 font-medium">{t(($) => $.permissions.project_permission_role)}</th>
                      <th className="px-3 py-2 font-medium">{t(($) => $.permissions.project_permission_granted_at)}</th>
                      <th className="w-12 px-3 py-2" />
                    </tr>
                  </thead>
                  <tbody>
                    {currentMembers.length === 0 ? (
                      <tr><td colSpan={5} className="px-3 py-6 text-center text-caption text-muted-foreground">{t(($) => $.permissions.current_access_empty)}</td></tr>
                    ) : currentMembers.map((member) => {
                      const profile = member.profile;
                      const name = profile?.name || member.user_id;
                      return (
                        <tr key={member.user_id} className="border-t">
                          <td className="px-3 py-2"><div className="font-medium">{name}</div>{profile?.email && <div className="text-caption text-muted-foreground">{profile.email}</div>}</td>
                          <td className="px-3 py-2 text-muted-foreground">{t(($) => $.permissions.project_member_source)}</td>
                          <td className="px-3 py-2">
                            {canManage ? (
                              <Select modal={false} items={roleItems} value={member.role as ProjectRole} onValueChange={(value) => value && void updateMemberRole(member.user_id, value)}>
                                <SelectTrigger className="w-32" aria-label={`${t(($) => $.permissions.change_role_aria)} ${name}`}><SelectValue /></SelectTrigger>
                                <SelectContent alignItemWithTrigger={false}>{roles.map((item) => <SelectItem key={item.key} value={item.key}>{item.name || item.key}</SelectItem>)}</SelectContent>
                              </Select>
                            ) : <span>{roleByKey.get(member.role)?.name || member.role || "—"}</span>}
                          </td>
                          <td className="px-3 py-2 text-muted-foreground">—</td>
                          <td className="px-3 py-2">{canManage && <Button variant="ghost" size="icon-sm" aria-label={`${t(($) => $.permissions.remove_aria)} ${name}`} disabled={removingId === member.user_id} onClick={() => void removeMember(member.user_id)}><UserMinus className="size-3.5" /></Button>}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </section>

          {canManage && <section role="region" aria-label={t(($) => $.permissions.add_members)} className="space-y-2 border-t pt-3">
            <div>
              <h3 className="text-body font-medium">{t(($) => $.permissions.add_members)}</h3>
              <p className="text-caption text-muted-foreground">{t(($) => $.permissions.add_members_description)}</p>
            </div>
            <div className="flex flex-col gap-3 sm:flex-row">
              {unifiedApi && <Select modal={false} items={[{ value: "user", label: t(($) => $.permissions.user) }, { value: "organization", label: t(($) => $.permissions.organization) }, { value: "everyone", label: t(($) => $.permissions.everyone) }]} value={subjectType} onValueChange={(value) => setSubjectType((value as Exclude<ProjectAccessGrantSubjectType, "role">) || "user")}><SelectTrigger className="w-full sm:w-36" aria-label={t(($) => $.permissions.dialog_title)}><SelectValue /></SelectTrigger><SelectContent alignItemWithTrigger={false}>{[{ value: "user", label: t(($) => $.permissions.user) }, { value: "organization", label: t(($) => $.permissions.organization) }, { value: "everyone", label: t(($) => $.permissions.everyone) }].map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent></Select>}
              {subjectType === "user" ? <ProjectMemberMultiSelect
                members={addableMembers}
                selectedIds={selectedIds}
                onToggle={toggleMember}
                onSelectAll={(userIds) => setSelectedIds((current) => new Set([...current, ...userIds]))}
                onClear={() => setSelectedIds(new Set())}
                placeholder={t(($) => $.permissions.search_placeholder)}
                selectedLabel={t(($) => $.permissions.selected_prefix)}
                selectAllLabel={t(($) => $.permissions.select_all)}
                clearLabel={t(($) => $.permissions.clear_selection)}
                noResultsLabel={t(($) => $.permissions.no_results)}
                loadingLabel={t(($) => $.permissions.loading)}
                errorLabel={t(($) => $.permissions.workspace_members_failed)}
                removeLabel={t(($) => $.permissions.remove_aria)}
                isLoading={membersLoading}
                hasError={membersError}
                ariaLabel={t(($) => $.permissions.search_placeholder)}
              /> : unifiedApi && subjectType === "organization" && workspaceId ? <div className="flex-1"><ProjectPermissionOrganizationSelect workspaceId={workspaceId} open={open} value={subjectIds} onValueChange={setSubjectIds} ariaLabel={t(($) => $.permissions.organization)} placeholder={t(($) => $.permissions.select_organization)} emptyLabel={t(($) => $.permissions.no_organizations)} /></div> : <div className="flex-1" />}
              <Select modal={false} items={roleItems} value={role} onValueChange={(value) => setRole(value ?? "member")}><SelectTrigger className="w-full sm:w-44" aria-label={t(($) => $.permissions.selected_role_aria)}><SelectValue /></SelectTrigger><SelectContent alignItemWithTrigger={false}>{roles.map((item) => <SelectItem key={item.key} value={item.key}>{item.name || item.key}</SelectItem>)}</SelectContent></Select>
              <Button className="shrink-0" onClick={() => void grantSelected()} disabled={!canGrant || granting}>
                {granting ? t(($) => $.permissions.granting) : t(($) => $.permissions.grant)}
              </Button>
            </div>
            <p className="text-caption text-muted-foreground">{roleByKey.get(role)?.description || t(($) => $.permissions.add_members_description)}</p>
          </section>}

          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>{t(($) => $.permissions.close)}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
