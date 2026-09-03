"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Search, ShieldCheck, UserMinus } from "lucide-react";
import { api } from "@multica/core/api";
import { useConfigStore } from "@multica/core/config";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
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
const PROJECT_PERMISSIONS = [
  { key: "project.view", label: "View project" },
  { key: "project.edit", label: "Edit project" },
  { key: "project.issue.create", label: "Create issues" },
  { key: "project.issue.manage", label: "Manage issues" },
  { key: "project.agent.use", label: "Use agents" },
  { key: "project.member.manage", label: "Manage members" },
  { key: "project.settings.manage", label: "Manage project settings" },
] as const;

function projectMembersKey(workspaceId: string, projectId: string) {
  return ["project-members", workspaceId, projectId] as const;
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
  const [search, setSearch] = useState("");
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [role, setRole] = useState<ProjectRole>("member");
  const [permission, setPermission] = useState<string>("project.view");
  const [subjectType, setSubjectType] = useState<ProjectAccessGrantSubjectType>("user");
  const [subjectId, setSubjectId] = useState("");
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
    enabled: !!projectId,
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
  const workspaceMemberByUser = useMemo(
    () => new Map(workspaceMembers.map((member) => [member.user_id, member])),
    [workspaceMembers],
  );
  const currentMembers = useMemo(
    () => projectMembers.map((member) => ({ ...member, profile: workspaceMemberByUser.get(member.user_id) })),
    [projectMembers, workspaceMemberByUser],
  );
  const addableMembers = useMemo(
    () => workspaceMembers.filter((member) => !roleByUser.has(member.user_id)),
    [roleByUser, workspaceMembers],
  );
  const filteredMembers = useMemo(() => {
    const needle = search.trim().toLocaleLowerCase();
    return [...addableMembers]
      .filter((member) => {
        if (!needle) return true;
        return `${member.name} ${member.email}`.toLocaleLowerCase().includes(needle);
      })
      .sort((left, right) => left.name.localeCompare(right.name));
  }, [addableMembers, search]);

  // 2026-09-01 coder(lq): The selected subject depends on the grant type;
  // organization, role, and everyone grants must not be blocked by the user
  // checklist left over from the previous subject type.
  const canGrant = subjectType === "user"
    ? selectedIds.size > 0
    : subjectType === "everyone"
      ? true
      : Boolean(subjectId.trim());

  useEffect(() => {
    if (open) return;
    setSearch("");
    setSelectedIds(new Set());
    setRole("member");
    setPermission("project.view");
    setSubjectType("user");
    setSubjectId("");
  }, [open]);

  useEffect(() => {
    // 2026-09-01 coder(lq): Clear stale selections when switching subject
    // types so a grant cannot accidentally submit an unrelated user or ID.
    setSelectedIds(new Set());
    setSubjectId("");
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
  const setMemberSelected = (userId: string, selected: boolean) => {
    setSelectedIds((current) => {
      const next = new Set(current);
      if (selected) next.add(userId);
      else next.delete(userId);
      return next;
    });
  };

  const grantSelected = async () => {
    if (unifiedApi) {
      const selectedSubjectIds = subjectType === "user" ? [...selectedIds] : [subjectType === "everyone" ? "" : subjectId.trim()];
      if ((subjectType === "user" && selectedSubjectIds.length === 0) || (subjectType !== "everyone" && !selectedSubjectIds[0])) return;
      setGranting(true);
      try {
        await Promise.all(selectedSubjectIds.map((id) => api.createProjectAccessGrant(projectId, {
          subject_type: subjectType,
          subject_id: id || undefined,
          ...(subjectType === "role" ? { permission } : { role }),
        })));
        await refreshGrants();
        setSelectedIds(new Set());
        setSubjectId("");
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
        await api.revokeProjectAccessGrant(projectId, {
          subject_type: "user",
          subject_id: userId,
          role: roleByUser.get(userId),
        });
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
      // 2026-08-31 coder(lq): Grants are immutable facts; changing a role is
      // represented as revoke + create so the audit trail and uniqueness key
      // remain deterministic across providers.
      await api.revokeProjectAccessGrant(projectId, {
        subject_type: grant.subject_type,
        subject_id: grant.subject_id,
        role: grant.role,
        permission: grant.permission,
      });
      await api.createProjectAccessGrant(projectId, {
        subject_type: grant.subject_type,
        subject_id: grant.subject_id,
        role: newRole,
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
  const allFilteredSelected = filteredMembers.length > 0 && filteredMembers.every((member) => selectedIds.has(member.user_id));

  return (
    <>
      {!hideTrigger && (
        <Button variant="outline" size="sm" className="shrink-0 gap-1.5" onClick={() => setOpen(true)}>
          <ShieldCheck className="size-3.5" />
          {t(($) => $.permissions.authorize)}
        </Button>
      )}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-2xl">
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
              accessGrantsQuery.data.grants.length === 0 ? (
                <div className="rounded-lg border border-dashed p-4 text-center text-body text-muted-foreground">{t(($) => $.permissions.current_access_empty)}</div>
              ) : (
                <div className="max-h-52 space-y-1 overflow-y-auto pr-1">
                  {accessGrantsQuery.data.grants.map((grant) => {
                    const subjectLabel = grant.subject_type === "everyone"
                      ? t(($) => $.permissions.everyone)
                      : grant.subject_type === "organization"
                        ? t(($) => $.permissions.organization_prefix, { name: grant.subject_id || "" })
                        : grant.subject_type === "role"
                          ? t(($) => $.permissions.role_prefix, { name: grant.subject_id || "" })
                          : t(($) => $.permissions.user);
                    const label = grant.subject_type === "user"
                      ? `${subjectLabel}: ${grant.subject_id || ""}`
                      : subjectLabel;
                    return <div key={grant.id || `${grant.subject_type}-${grant.subject_id}-${grant.role}-${grant.permission}`} className="flex items-center gap-3 rounded-lg border px-2 py-2">
                      <div className="min-w-0 flex-1"><div className="truncate text-body font-medium">{label}</div><div className="truncate text-caption text-muted-foreground">{grant.role || grant.permission || grant.source}</div></div>
                      {canManage && grant.subject_type === "user" && grant.role && <Select items={roleItems} value={grant.role} onValueChange={(value) => value && void updateGrantRole(grant, value)}><SelectTrigger className="w-28" aria-label={`${t(($) => $.permissions.change_role_aria)} ${label}`}><SelectValue /></SelectTrigger><SelectContent>{roles.map((item) => <SelectItem key={item.key} value={item.key}>{item.name || item.key}</SelectItem>)}</SelectContent></Select>}
                      {canManage && <Button variant="ghost" size="icon-sm" aria-label={`${t(($) => $.permissions.remove_aria)} ${label}`} onClick={() => void removeGrant(grant)}><UserMinus className="size-3.5" /></Button>}
                    </div>;
                  })}
                </div>
              )
            ) : projectMembersQuery.isLoading ? (
              <div className="py-6 text-center text-body text-muted-foreground">{t(($) => $.permissions.loading)}</div>
            ) : currentMembers.length === 0 ? (
              <div className="rounded-lg border border-dashed p-4 text-center text-body text-muted-foreground">{t(($) => $.permissions.current_access_empty)}</div>
            ) : (
              <div className="max-h-52 space-y-1 overflow-y-auto pr-1">
                {currentMembers.map((member) => {
                  const profile = member.profile;
                  const name = profile?.name || member.user_id;
                  return (
                    <div key={member.user_id} className="flex items-center gap-3 rounded-lg border px-2 py-2">
                      <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-caption font-medium">{name.slice(0, 1).toLocaleUpperCase()}</div>
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-body font-medium">{name}</div>
                        {profile?.email && <div className="truncate text-caption text-muted-foreground">{profile.email}</div>}
                      </div>
                      <Select items={roleItems} value={member.role as ProjectRole} onValueChange={(value) => value && void updateMemberRole(member.user_id, value)}>
                        <SelectTrigger className="w-28" aria-label={`${t(($) => $.permissions.change_role_aria)} ${name}`}><SelectValue /></SelectTrigger>
                        <SelectContent>{roles.map((item) => <SelectItem key={item.key} value={item.key}>{item.name || item.key}</SelectItem>)}</SelectContent>
                      </Select>
                      <Button variant="ghost" size="icon-sm" aria-label={`${t(($) => $.permissions.remove_aria)} ${name}`} disabled={removingId === member.user_id} onClick={() => void removeMember(member.user_id)}><UserMinus className="size-3.5" /></Button>
                    </div>
                  );
                })}
              </div>
            )}
          </section>

          <section role="region" aria-label={t(($) => $.permissions.add_members)} className="space-y-2">
            <div>
              <h3 className="text-body font-medium">{t(($) => $.permissions.add_members)}</h3>
              <p className="text-caption text-muted-foreground">{t(($) => $.permissions.add_members_description)}</p>
            </div>
            <div className="flex flex-col gap-3 sm:flex-row">
              <div className="relative flex-1"><Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" /><Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder={t(($) => $.permissions.search_placeholder)} aria-label={t(($) => $.permissions.search_placeholder)} className="pl-8" /></div>
              {unifiedApi && <Select items={[{ value: "user", label: t(($) => $.permissions.user) }, { value: "organization", label: t(($) => $.permissions.organization) }, { value: "role", label: t(($) => $.permissions.role) }, { value: "everyone", label: t(($) => $.permissions.everyone) }]} value={subjectType} onValueChange={(value) => setSubjectType((value as ProjectAccessGrantSubjectType) || "user")}><SelectTrigger className="w-full sm:w-36" aria-label={t(($) => $.permissions.dialog_title)}><SelectValue /></SelectTrigger><SelectContent>{[{ value: "user", label: t(($) => $.permissions.user) }, { value: "organization", label: t(($) => $.permissions.organization) }, { value: "role", label: t(($) => $.permissions.role) }, { value: "everyone", label: t(($) => $.permissions.everyone) }].map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent></Select>}
              {unifiedApi && subjectType === "organization" && workspaceId ? <ProjectPermissionOrganizationSelect workspaceId={workspaceId} open={open} value={subjectId} onValueChange={setSubjectId} ariaLabel={t(($) => $.permissions.organization)} placeholder={t(($) => $.permissions.select_organization)} emptyLabel={t(($) => $.permissions.no_organizations)} /> : unifiedApi && subjectType === "role" ? <Select items={roleItems} value={subjectId} onValueChange={(value) => setSubjectId(value ?? "")}><SelectTrigger className="w-full sm:w-44" aria-label={t(($) => $.permissions.role_key)}><SelectValue placeholder={t(($) => $.permissions.role_key)} /></SelectTrigger><SelectContent>{roles.map((item) => <SelectItem key={item.key} value={item.key}>{item.name || item.key}</SelectItem>)}</SelectContent></Select> : null}
              {subjectType === "role" ? <Select items={PROJECT_PERMISSIONS.map((item) => ({ value: item.key, label: item.label }))} value={permission} onValueChange={(value) => setPermission(value ?? "project.view")}><SelectTrigger className="w-full sm:w-52" aria-label={t(($) => $.permissions.single_permission)}><SelectValue /></SelectTrigger><SelectContent>{PROJECT_PERMISSIONS.map((item) => <SelectItem key={item.key} value={item.key}>{item.label}</SelectItem>)}</SelectContent></Select> : <Select items={roleItems} value={role} onValueChange={(value) => setRole(value ?? "member")}><SelectTrigger className="w-full sm:w-44" aria-label={t(($) => $.permissions.selected_role_aria)}><SelectValue /></SelectTrigger><SelectContent>{roles.map((item) => <SelectItem key={item.key} value={item.key}>{item.name || item.key}</SelectItem>)}</SelectContent></Select>}
            </div>
            <p className="text-caption text-muted-foreground">{subjectType === "role" ? t(($) => $.permissions.single_permission) : roleByKey.get(role)?.description || t(($) => $.permissions.add_members_description)}</p>
            <div className="flex items-center justify-between border-b pb-2 text-caption"><span className="text-muted-foreground">{t(($) => $.permissions.selected_prefix)} {selectedIds.size}</span><div className="flex items-center gap-1"><Button variant="ghost" size="sm" onClick={() => setSelectedIds(new Set(filteredMembers.map((member) => member.user_id)))} disabled={allFilteredSelected || filteredMembers.length === 0}>{t(($) => $.permissions.select_all)}</Button><Button variant="ghost" size="sm" onClick={() => setSelectedIds(new Set())} disabled={selectedIds.size === 0}>{t(($) => $.permissions.clear_selection)}</Button></div></div>
            <div className="max-h-52 space-y-1 overflow-y-auto pr-1">
              {membersLoading ? <div className="py-6 text-center text-body text-muted-foreground">{t(($) => $.permissions.loading)}</div> : membersError ? <div className="py-6 text-center text-body text-destructive">{t(($) => $.permissions.workspace_members_failed)}</div> : filteredMembers.length === 0 ? <div className="py-6 text-center text-body text-muted-foreground">{t(($) => $.permissions.no_results)}</div> : filteredMembers.map((member) => <div key={member.user_id} className="flex cursor-pointer items-center gap-3 rounded-lg px-2 py-2 hover:bg-accent/60" onClick={(event) => { if ((event.target as HTMLElement).closest('[data-slot="checkbox"]')) return; toggleMember(member.user_id); }}><Checkbox checked={selectedIds.has(member.user_id)} aria-label={member.name} onCheckedChange={(nextChecked) => setMemberSelected(member.user_id, nextChecked)} /><div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-caption font-medium">{(member.name || member.email || "?").slice(0, 1).toLocaleUpperCase()}</div><div className="min-w-0 flex-1"><div className="truncate text-body font-medium">{member.name || member.email}</div>{member.name && member.email && <div className="truncate text-caption text-muted-foreground">{member.email}</div>}</div></div>)}
            </div>
          </section>

          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>{t(($) => $.permissions.cancel)}</Button>
      <Button onClick={() => void grantSelected()} disabled={!canGrant || granting}>
              {granting ? t(($) => $.permissions.granting) : t(($) => $.permissions.grant)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
